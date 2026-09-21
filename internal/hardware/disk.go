package hardware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Disk struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Model       string   `json:"model"`
	Serial      string   `json:"serial"`
	Size        uint64   `json:"size"`
	Transport   string   `json:"transport"`
	Temperature *float64 `json:"temperature,omitempty"`
	State       string   `json:"state"`
	Error       string   `json:"error,omitempty"`
}

type DiskService struct {
	LSBLKPath    string
	SmartctlPath string
	Timeout      time.Duration
	DevRoot      string
}

func NewDiskService() *DiskService {
	return &DiskService{
		LSBLKPath:    "/usr/bin/lsblk",
		SmartctlPath: "/usr/sbin/smartctl",
		Timeout:      15 * time.Second,
		DevRoot:      "/dev",
	}
}

type lsblkOutput struct {
	BlockDevices []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Type   string `json:"type"`
		Model  string `json:"model"`
		Serial string `json:"serial"`
		Size   uint64 `json:"size"`
		Tran   string `json:"tran"`
	} `json:"blockdevices"`
}

func (service *DiskService) Discover(ctx context.Context) ([]Disk, error) {
	commandCtx, cancel := context.WithTimeout(ctx, service.Timeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, service.LSBLKPath, "--json", "--bytes", "--output", "NAME,PATH,TYPE,MODEL,SERIAL,SIZE,TRAN").Output()
	if err != nil {
		return nil, fmt.Errorf("run lsblk: %w", err)
	}
	var parsed lsblkOutput
	if err := json.Unmarshal(output, &parsed); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	stableIDs := service.stableIDs()
	disks := make([]Disk, 0, len(parsed.BlockDevices))
	for _, device := range parsed.BlockDevices {
		if device.Type != "disk" || strings.HasPrefix(device.Name, "loop") || strings.HasPrefix(device.Name, "ram") {
			continue
		}
		id := stableIDs[device.Path]
		if id == "" {
			id = device.Path
		}
		disks = append(disks, Disk{
			ID: id, Path: device.Path, Model: strings.TrimSpace(device.Model), Serial: strings.TrimSpace(device.Serial),
			Size: device.Size, Transport: device.Tran, State: "unknown",
		})
	}
	return disks, nil
}

func (service *DiskService) stableIDs() map[string]string {
	result := make(map[string]string)
	entries, err := os.ReadDir(filepath.Join(service.DevRoot, "disk", "by-id"))
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-part") {
			continue
		}
		link := filepath.Join(service.DevRoot, "disk", "by-id", entry.Name())
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			continue
		}
		if _, exists := result[target]; !exists || strings.HasPrefix(entry.Name(), "ata-") || strings.HasPrefix(entry.Name(), "nvme-") {
			result[target] = link
		}
	}
	return result
}

type smartOutput struct {
	Temperature struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerMode string `json:"power_mode"`
	Device    struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
}

func (service *DiskService) Temperature(ctx context.Context, device string, avoidWakeup bool) (float64, string, error) {
	arguments := []string{"-aj"}
	if avoidWakeup {
		arguments = append([]string{"-n", "standby"}, arguments...)
	}
	arguments = append(arguments, device)
	commandCtx, cancel := context.WithTimeout(ctx, service.Timeout)
	defer cancel()
	output, commandErr := exec.CommandContext(commandCtx, service.SmartctlPath, arguments...).CombinedOutput()
	var parsed smartOutput
	if err := json.Unmarshal(output, &parsed); err != nil {
		if commandErr != nil {
			return 0, "error", fmt.Errorf("run smartctl: %w", commandErr)
		}
		return 0, "error", fmt.Errorf("parse smartctl output: %w", err)
	}
	powerMode := strings.ToLower(parsed.PowerMode)
	if strings.Contains(powerMode, "standby") || strings.Contains(powerMode, "sleep") {
		return 0, "standby", nil
	}
	if parsed.Temperature.Current == nil {
		if commandErr != nil {
			return 0, "error", fmt.Errorf("smartctl did not return a temperature: %w", commandErr)
		}
		return 0, "error", errors.New("smartctl did not return a temperature")
	}
	return *parsed.Temperature.Current, "active", nil
}
