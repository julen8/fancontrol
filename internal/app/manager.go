package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/julen8/fancontrol/internal/config"
	"github.com/julen8/fancontrol/internal/control"
	"github.com/julen8/fancontrol/internal/hardware"
)

type CPU interface {
	MaxTemperature(context.Context) (float64, error)
}

type Disks interface {
	Discover(context.Context) ([]hardware.Disk, error)
	Temperature(context.Context, string, bool) (float64, string, error)
}

type FanDriver interface {
	IsFullMode(context.Context) (bool, error)
	SetFullMode(context.Context) error
	SetZoneSpeed(context.Context, int, int) error
	ReadFans(context.Context) ([]hardware.FanReading, error)
}

type ControllerState struct {
	Enabled      bool      `json:"enabled"`
	Mode         string    `json:"mode"`
	Temperature  *float64  `json:"temperature,omitempty"`
	TargetSpeed  int       `json:"target_speed"`
	AppliedSpeed int       `json:"applied_speed"`
	Failures     int       `json:"failures"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Status struct {
	StartedAt time.Time             `json:"started_at"`
	FullMode  bool                  `json:"full_mode"`
	IPMIError string                `json:"ipmi_error,omitempty"`
	FanError  string                `json:"fan_error,omitempty"`
	Fans      []hardware.FanReading `json:"fans"`
	CPU       ControllerState       `json:"cpu"`
	HDD       ControllerState       `json:"hdd"`
	Disks     []hardware.Disk       `json:"disks"`
}

type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Manager struct {
	configPath string
	cpu        CPU
	disks      Disks
	fan        FanDriver
	logger     *slog.Logger

	configMu sync.RWMutex
	updateMu sync.Mutex
	config   config.Config
	stateMu  sync.RWMutex
	status   Status
	events   []Event

	cpuUpdate chan struct{}
	hddUpdate chan struct{}
	testMu    sync.Mutex
	testZones map[int]context.CancelFunc
	testWG    sync.WaitGroup
	runCtx    context.Context
	stopping  bool
}

func New(configuration config.Config, configPath string, cpu CPU, disks Disks, fan FanDriver, logger *slog.Logger) *Manager {
	return &Manager{
		configPath: configPath,
		config:     configuration,
		cpu:        cpu,
		disks:      disks,
		fan:        fan,
		logger:     logger,
		status: Status{
			StartedAt: time.Now(),
			CPU:       ControllerState{Status: "starting"},
			HDD:       ControllerState{Status: "starting"},
		},
		cpuUpdate: make(chan struct{}, 1),
		hddUpdate: make(chan struct{}, 1),
		testZones: make(map[int]context.CancelFunc),
	}
}

func (manager *Manager) Config() config.Config {
	manager.configMu.RLock()
	defer manager.configMu.RUnlock()
	return manager.config
}

func (manager *Manager) UpdateConfig(configuration config.Config) error {
	manager.updateMu.Lock()
	defer manager.updateMu.Unlock()
	manager.configMu.RLock()
	configuration.Server.Auth.PasswordHash = manager.config.Server.Auth.PasswordHash
	manager.configMu.RUnlock()
	if err := config.Validate(configuration); err != nil {
		return err
	}
	if err := config.Save(manager.configPath, configuration); err != nil {
		return err
	}
	manager.configMu.Lock()
	manager.config = configuration
	manager.configMu.Unlock()
	manager.notify(manager.cpuUpdate)
	manager.notify(manager.hddUpdate)
	manager.addEvent("info", "configuration saved and applied")
	return nil
}

func (manager *Manager) UpdatePassword(hash string) error {
	manager.updateMu.Lock()
	defer manager.updateMu.Unlock()
	configuration := manager.Config()
	configuration.Server.Auth.PasswordHash = hash
	if err := config.Save(manager.configPath, configuration); err != nil {
		return err
	}
	manager.configMu.Lock()
	manager.config = configuration
	manager.configMu.Unlock()
	manager.addEvent("info", "web password changed")
	return nil
}

func (manager *Manager) notify(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (manager *Manager) Status() Status {
	manager.stateMu.RLock()
	defer manager.stateMu.RUnlock()
	status := manager.status
	status.Fans = append([]hardware.FanReading(nil), manager.status.Fans...)
	status.Disks = append([]hardware.Disk(nil), manager.status.Disks...)
	return status
}

func (manager *Manager) Events() []Event {
	manager.stateMu.RLock()
	defer manager.stateMu.RUnlock()
	return append([]Event(nil), manager.events...)
}

func (manager *Manager) DiscoverDisks(ctx context.Context) ([]hardware.Disk, error) {
	disks, err := manager.disks.Discover(ctx)
	if err != nil {
		return nil, err
	}
	current := manager.Status().Disks
	for index := range disks {
		for _, known := range current {
			if disks[index].ID == known.ID {
				disks[index].Temperature = known.Temperature
				disks[index].State = known.State
				disks[index].Error = known.Error
			}
		}
	}
	return disks, nil
}

func (manager *Manager) Run(ctx context.Context) {
	manager.testMu.Lock()
	manager.runCtx = ctx
	manager.testMu.Unlock()
	manager.ensureFullMode(ctx)
	var workers sync.WaitGroup
	workers.Add(4)
	go func() { defer workers.Done(); manager.runCPU(ctx) }()
	go func() { defer workers.Done(); manager.runHDD(ctx) }()
	go func() { defer workers.Done(); manager.enforceFullMode(ctx) }()
	go func() { defer workers.Done(); manager.monitorFans(ctx) }()
	<-ctx.Done()
	manager.testMu.Lock()
	manager.stopping = true
	for _, cancel := range manager.testZones {
		cancel()
	}
	manager.testMu.Unlock()
	workers.Wait()
	manager.testWG.Wait()
	manager.shutdownFans()
}

func (manager *Manager) monitorFans(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		readings, err := manager.fan.ReadFans(ctx)
		manager.stateMu.Lock()
		if err != nil {
			manager.status.FanError = err.Error()
		} else {
			manager.status.Fans = readings
			manager.status.FanError = ""
		}
		manager.stateMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (manager *Manager) runCPU(ctx context.Context) {
	var history []float64
	failures := 0
	for {
		configuration := manager.Config().CPU
		manager.updateController("cpu", func(state *ControllerState) {
			state.Enabled = configuration.Enabled
			state.Mode = configuration.Mode
		})
		if configuration.Enabled {
			temperature, err := manager.cpu.MaxTemperature(ctx)
			if err != nil {
				failures++
				manager.handleReadFailure(ctx, "cpu", configuration, failures, err)
			} else {
				failures = 0
				history = append(history, temperature)
				if len(history) > configuration.SmoothingSamples {
					history = history[len(history)-configuration.SmoothingSamples:]
				}
				manager.applyTemperature(ctx, "cpu", configuration, average(history))
			}
		} else {
			manager.updateController("cpu", func(state *ControllerState) {
				state.Status = "disabled"
				state.Error = ""
				state.UpdatedAt = time.Now()
			})
		}
		if !manager.wait(ctx, configuration.PollInterval.Duration, manager.cpuUpdate) {
			return
		}
	}
}

func (manager *Manager) runHDD(ctx context.Context) {
	var history []float64
	lastKnown := make(map[string]float64)
	failures := 0
	for {
		configuration := manager.Config().HDD
		manager.updateController("hdd", func(state *ControllerState) {
			state.Enabled = configuration.Enabled
			state.Mode = configuration.Mode
		})
		if !configuration.Enabled {
			manager.updateController("hdd", func(state *ControllerState) {
				state.Status = "disabled"
				state.Error = ""
				state.UpdatedAt = time.Now()
			})
		} else if len(configuration.Devices) == 0 {
			manager.applySpeed(ctx, "hdd", configuration.ControllerConfig, configuration.SafeSpeed, "failsafe", nil, "no disks selected")
		} else {
			temperature, diskStates, err := manager.readDisks(ctx, configuration, lastKnown)
			manager.stateMu.Lock()
			manager.status.Disks = diskStates
			manager.stateMu.Unlock()
			if err != nil && len(configuration.Devices) > 0 {
				failures++
				manager.handleReadFailure(ctx, "hdd", configuration.ControllerConfig, failures, err)
			} else {
				failures = 0
				if temperature != nil {
					history = append(history, *temperature)
					if len(history) > configuration.SmoothingSamples {
						history = history[len(history)-configuration.SmoothingSamples:]
					}
					manager.applyTemperature(ctx, "hdd", configuration.ControllerConfig, average(history))
				} else {
					manager.applyFixed(ctx, "hdd", configuration.ControllerConfig)
				}
			}
		}
		if !manager.wait(ctx, configuration.PollInterval.Duration, manager.hddUpdate) {
			return
		}
	}
}

func (manager *Manager) readDisks(ctx context.Context, configuration config.HDDConfig, lastKnown map[string]float64) (*float64, []hardware.Disk, error) {
	discovered, discoverErr := manager.disks.Discover(ctx)
	byID := make(map[string]hardware.Disk, len(discovered))
	for _, disk := range discovered {
		byID[disk.ID] = disk
	}
	states := make([]hardware.Disk, 0, len(configuration.Devices))
	var maximum *float64
	var readErrors []error
	for _, id := range configuration.Devices {
		disk, exists := byID[id]
		if !exists {
			disk = hardware.Disk{ID: id, Path: id, State: "missing", Error: "disk is not present"}
			readErrors = append(readErrors, fmt.Errorf("disk %s is not present", id))
			states = append(states, disk)
			continue
		}
		temperature, state, err := manager.disks.Temperature(ctx, id, configuration.AvoidWakeup)
		disk.State = state
		if err != nil {
			disk.Error = err.Error()
			readErrors = append(readErrors, err)
		} else if state == "standby" {
			if previous, ok := lastKnown[id]; ok {
				disk.Temperature = &previous
			}
		} else {
			lastKnown[id] = temperature
			disk.Temperature = &temperature
		}
		if disk.Temperature != nil && (maximum == nil || *disk.Temperature > *maximum) {
			value := *disk.Temperature
			maximum = &value
		}
		states = append(states, disk)
	}
	if maximum == nil && len(configuration.Devices) > 0 {
		if len(readErrors) > 0 {
			return nil, states, errors.Join(readErrors...)
		}
		return nil, states, errors.New("selected disks have no current or cached temperature")
	}
	if len(readErrors) > 0 {
		return maximum, states, errors.Join(readErrors...)
	}
	if discoverErr != nil {
		return maximum, states, discoverErr
	}
	return maximum, states, nil
}

func (manager *Manager) applyTemperature(ctx context.Context, name string, configuration config.ControllerConfig, temperature float64) {
	speed := configuration.FixedSpeed
	status := "fixed"
	if configuration.Mode == control.ModeCurve {
		speed = control.SpeedForTemperature(configuration.Curve, temperature)
		status = "curve"
	}
	if temperature >= configuration.CriticalTemperature {
		speed = configuration.SafeSpeed
		status = "critical"
		manager.addEvent("error", fmt.Sprintf("%s temperature %.1fC reached critical threshold", name, temperature))
	}
	manager.applySpeed(ctx, name, configuration, speed, status, &temperature, "")
}

func (manager *Manager) applyFixed(ctx context.Context, name string, configuration config.ControllerConfig) {
	manager.applySpeed(ctx, name, configuration, configuration.FixedSpeed, "fixed", nil, "")
}

func (manager *Manager) handleReadFailure(ctx context.Context, name string, configuration config.ControllerConfig, failures int, readErr error) {
	if failures >= configuration.ReadFailureLimit {
		manager.applySpeed(ctx, name, configuration, configuration.SafeSpeed, "failsafe", nil, readErr.Error())
		manager.updateController(name, func(state *ControllerState) {
			state.Failures = failures
		})
		return
	}
	manager.updateController(name, func(state *ControllerState) {
		state.Failures = failures
		state.Status = "sensor warning"
		state.Error = readErr.Error()
		state.UpdatedAt = time.Now()
	})
}

func (manager *Manager) applySpeed(ctx context.Context, name string, configuration config.ControllerConfig, speed int, status string, temperature *float64, detail string) {
	speed = max(configuration.MinimumSpeed, min(100, speed))
	current := manager.Status()
	previous := current.CPU.AppliedSpeed
	if name == "hdd" {
		previous = current.HDD.AppliedSpeed
	}
	applyErr := error(nil)
	testingZone := manager.zoneUnderTest(configuration.Zone)
	if previous != speed && !testingZone {
		applyErr = manager.fan.SetZoneSpeed(ctx, configuration.Zone, speed)
	}
	manager.updateController(name, func(state *ControllerState) {
		state.Temperature = temperature
		state.TargetSpeed = speed
		state.Failures = 0
		state.Status = status
		state.Error = detail
		if applyErr == nil && !testingZone {
			state.AppliedSpeed = speed
		} else {
			state.Error = applyErr.Error()
			state.Status = "ipmi error"
		}
		state.UpdatedAt = time.Now()
	})
	if applyErr != nil {
		manager.addEvent("error", fmt.Sprintf("%s fan: %v", name, applyErr))
	} else if previous != speed && !testingZone {
		manager.addEvent("info", fmt.Sprintf("%s fan set to %d%%", name, speed))
	}
}

func (manager *Manager) updateController(name string, update func(*ControllerState)) {
	manager.stateMu.Lock()
	defer manager.stateMu.Unlock()
	if name == "cpu" {
		update(&manager.status.CPU)
	} else {
		update(&manager.status.HDD)
	}
}

func (manager *Manager) ensureFullMode(ctx context.Context) {
	full, err := manager.fan.IsFullMode(ctx)
	if err == nil && !full {
		err = manager.fan.SetFullMode(ctx)
		full = err == nil
	}
	manager.stateMu.Lock()
	manager.status.FullMode = full
	if err != nil {
		manager.status.IPMIError = err.Error()
	} else {
		manager.status.IPMIError = ""
	}
	manager.stateMu.Unlock()
	if err != nil {
		manager.addEvent("error", err.Error())
	}
}

func (manager *Manager) enforceFullMode(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if manager.Config().IPMI.EnforceFullMode {
				manager.ensureFullMode(ctx)
			}
		}
	}
}

func (manager *Manager) shutdownFans() {
	configuration := manager.Config()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), configuration.IPMI.Timeout.Duration*3)
	defer cancel()
	if err := manager.fan.SetFullMode(shutdownCtx); err != nil {
		manager.logger.Error("failed to enforce FULL mode during shutdown", "error", err)
	}
	zones := []int{configuration.CPU.Zone, configuration.HDD.Zone}
	for _, zone := range slices.Compact(zones) {
		if err := manager.fan.SetZoneSpeed(shutdownCtx, zone, configuration.IPMI.ExitSpeed); err != nil {
			manager.logger.Error("failed to apply shutdown fan speed", "zone", zone, "error", err)
		}
	}
}

func (manager *Manager) TestFan(ctx context.Context, zone, speed int, duration time.Duration) error {
	if duration < time.Second || duration > 30*time.Second {
		return errors.New("test duration must be between 1 and 30 seconds")
	}
	manager.testMu.Lock()
	if manager.runCtx == nil || manager.runCtx.Err() != nil || manager.stopping {
		manager.testMu.Unlock()
		return errors.New("fan controller is not running")
	}
	if _, exists := manager.testZones[zone]; exists {
		manager.testMu.Unlock()
		return errors.New("this fan zone is already being tested")
	}
	testCtx, cancel := context.WithCancel(manager.runCtx)
	manager.testZones[zone] = cancel
	manager.testWG.Add(1)
	manager.testMu.Unlock()
	if err := manager.fan.SetZoneSpeed(ctx, zone, speed); err != nil {
		manager.finishFanTest(zone)
		manager.testWG.Done()
		return err
	}
	go func() {
		defer manager.testWG.Done()
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-testCtx.Done():
			manager.finishFanTest(zone)
			return
		}
		status := manager.Status()
		restore := status.CPU.TargetSpeed
		if zone == manager.Config().HDD.Zone {
			restore = status.HDD.TargetSpeed
		}
		if restore == 0 {
			restore = manager.Config().IPMI.ExitSpeed
		}
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), manager.Config().IPMI.Timeout.Duration)
		defer restoreCancel()
		if err := manager.fan.SetZoneSpeed(restoreCtx, zone, restore); err != nil {
			manager.addEvent("error", fmt.Sprintf("restore fan test: %v", err))
		}
		manager.finishFanTest(zone)
	}()
	return nil
}

func (manager *Manager) zoneUnderTest(zone int) bool {
	manager.testMu.Lock()
	defer manager.testMu.Unlock()
	_, exists := manager.testZones[zone]
	return exists
}

func (manager *Manager) finishFanTest(zone int) {
	manager.testMu.Lock()
	defer manager.testMu.Unlock()
	if cancel, exists := manager.testZones[zone]; exists {
		cancel()
		delete(manager.testZones, zone)
	}
}

func (manager *Manager) wait(ctx context.Context, duration time.Duration, update <-chan struct{}) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-update:
		return true
	case <-timer.C:
		return true
	}
}

func (manager *Manager) addEvent(level, message string) {
	event := Event{Time: time.Now(), Level: level, Message: message}
	manager.logger.Log(context.Background(), slog.LevelInfo, message, "level", level)
	manager.stateMu.Lock()
	defer manager.stateMu.Unlock()
	manager.events = append([]Event{event}, manager.events...)
	if len(manager.events) > 100 {
		manager.events = manager.events[:100]
	}
}

func average(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}
