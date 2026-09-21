package hardware

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type CPUReader struct {
	Root string
}

func NewCPUReader() *CPUReader {
	return &CPUReader{Root: "/sys/class/hwmon"}
}

func (reader *CPUReader) MaxTemperature(_ context.Context) (float64, error) {
	hwmons, err := filepath.Glob(filepath.Join(reader.Root, "hwmon*"))
	if err != nil {
		return 0, err
	}
	var maximum float64
	found := false
	for _, hwmon := range hwmons {
		nameBytes, err := os.ReadFile(filepath.Join(hwmon, "name"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(nameBytes))
		if name != "coretemp" && name != "k10temp" && name != "zenpower" {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(hwmon, "temp*_input"))
		for _, input := range inputs {
			valueBytes, err := os.ReadFile(input)
			if err != nil {
				continue
			}
			millidegrees, err := strconv.ParseFloat(strings.TrimSpace(string(valueBytes)), 64)
			if err != nil || millidegrees <= 0 || math.IsNaN(millidegrees) || math.IsInf(millidegrees, 0) {
				continue
			}
			temperature := millidegrees / 1000
			if !found || temperature > maximum {
				maximum = temperature
				found = true
			}
		}
	}
	if !found {
		return 0, errors.New("no readable coretemp, k10temp, or zenpower sensor found")
	}
	if maximum > 120 {
		return 0, fmt.Errorf("invalid CPU temperature %.1fC", maximum)
	}
	return maximum, nil
}
