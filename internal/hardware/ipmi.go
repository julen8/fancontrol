package hardware

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type IPMI struct {
	mu      sync.RWMutex
	Command string
	Timeout time.Duration
}

type FanReading struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	RPM    int    `json:"rpm"`
	Status string `json:"status"`
}

func NewIPMI(command string, timeout time.Duration) *IPMI {
	return &IPMI{Command: command, Timeout: timeout}
}

func (ipmi *IPMI) Configure(command string, timeout time.Duration) {
	ipmi.mu.Lock()
	defer ipmi.mu.Unlock()
	ipmi.Command = command
	ipmi.Timeout = timeout
}

func (ipmi *IPMI) run(ctx context.Context, arguments ...string) ([]byte, error) {
	ipmi.mu.RLock()
	commandPath := ipmi.Command
	timeout := ipmi.Timeout
	ipmi.mu.RUnlock()
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, commandPath, arguments...).CombinedOutput()
	if commandCtx.Err() != nil {
		return output, fmt.Errorf("ipmitool timeout: %w", commandCtx.Err())
	}
	if err != nil {
		return output, fmt.Errorf("ipmitool %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (ipmi *IPMI) IsFullMode(ctx context.Context) (bool, error) {
	output, err := ipmi.run(ctx, "raw", "0x30", "0x45", "0x00")
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(output))
	return len(fields) > 0 && strings.EqualFold(fields[len(fields)-1], "01"), nil
}

func (ipmi *IPMI) SetFullMode(ctx context.Context) error {
	_, err := ipmi.run(ctx, "raw", "0x30", "0x45", "0x01", "0x01")
	return err
}

func (ipmi *IPMI) SetZoneSpeed(ctx context.Context, zone, speed int) error {
	if zone < 0 || zone > 255 || speed < 1 || speed > 100 {
		return fmt.Errorf("invalid zone %d or speed %d", zone, speed)
	}
	_, err := ipmi.run(ctx, "raw", "0x30", "0x70", "0x66", "0x01", strconv.Itoa(zone), strconv.Itoa(speed))
	return err
}

func (ipmi *IPMI) ReadFans(ctx context.Context) ([]FanReading, error) {
	output, err := ipmi.run(ctx, "sensor")
	if err != nil {
		return nil, err
	}
	return parseFanSensors(string(output)), nil
}

func parseFanSensors(output string) []FanReading {
	readings := make([]FanReading, 0)
	for line := range strings.Lines(output) {
		fields := strings.Split(line, "|")
		if len(fields) < 4 || !strings.EqualFold(strings.TrimSpace(fields[2]), "RPM") {
			continue
		}
		name := strings.TrimSpace(fields[0])
		value, err := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		upperName := strings.ToUpper(name)
		group := "other"
		if strings.Contains(upperName, "CPU") {
			group = "cpu"
		} else if strings.Contains(upperName, "SYS") {
			group = "system"
		}
		readings = append(readings, FanReading{
			Name: name, Group: group, RPM: int(math.Round(value)), Status: strings.TrimSpace(fields[3]),
		})
	}
	return readings
}
