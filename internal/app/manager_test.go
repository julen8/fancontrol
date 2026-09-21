package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/julen8/fancontrol/internal/config"
	"github.com/julen8/fancontrol/internal/hardware"
)

type failingCPU struct{}

func (failingCPU) MaxTemperature(context.Context) (float64, error) {
	return 0, errors.New("sensor unavailable")
}

type emptyDisks struct{}

func (emptyDisks) Discover(context.Context) ([]hardware.Disk, error) { return nil, nil }
func (emptyDisks) Temperature(context.Context, string, bool) (float64, string, error) {
	return 0, "error", errors.New("not implemented")
}

type recordingFan struct {
	mu    sync.Mutex
	calls []fanCall
}

type fanCall struct{ zone, speed int }

func (fan *recordingFan) IsFullMode(context.Context) (bool, error) { return true, nil }
func (fan *recordingFan) SetFullMode(context.Context) error        { return nil }
func (fan *recordingFan) ReadFans(context.Context) ([]hardware.FanReading, error) {
	return []hardware.FanReading{{Name: "CPU_FAN1", Group: "cpu", RPM: 1200, Status: "ok"}}, nil
}
func (fan *recordingFan) SetZoneSpeed(_ context.Context, zone, speed int) error {
	fan.mu.Lock()
	defer fan.mu.Unlock()
	fan.calls = append(fan.calls, fanCall{zone: zone, speed: speed})
	return nil
}

func TestManagerCollectsFanReadings(t *testing.T) {
	configuration := config.Default()
	configuration.CPU.Enabled = false
	configuration.HDD.Enabled = false
	manager := New(configuration, t.TempDir()+"/config.toml", failingCPU{}, emptyDisks{}, &recordingFan{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for len(manager.Status().Fans) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fans := manager.Status().Fans; len(fans) != 1 || fans[0].RPM != 1200 {
		t.Fatalf("unexpected fan readings: %#v", fans)
	}
	cancel()
	<-done
}

func TestCPUFailuresApplySafeSpeed(t *testing.T) {
	configuration := config.Default()
	configuration.Server.Auth.Enabled = false
	configuration.CPU.PollInterval = config.Duration{Duration: 5 * time.Millisecond}
	configuration.CPU.ReadFailureLimit = 2
	configuration.HDD.Enabled = false
	fan := &recordingFan{}
	manager := New(configuration, t.TempDir()+"/config.toml", failingCPU{}, emptyDisks{}, fan, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	manager.Run(ctx)

	fan.mu.Lock()
	defer fan.mu.Unlock()
	foundSafe := false
	for _, call := range fan.calls {
		if call.zone == configuration.CPU.Zone && call.speed == configuration.CPU.SafeSpeed {
			foundSafe = true
		}
	}
	if !foundSafe {
		t.Fatalf("safe speed was not applied; calls: %#v", fan.calls)
	}
}
