package control

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	ModeCurve = "curve"
	ModeFixed = "fixed"
)

type Point struct {
	Temperature float64 `toml:"temperature" json:"temperature"`
	Speed       int     `toml:"speed" json:"speed"`
}

func Validate(mode string, fixedSpeed, minimumSpeed int, points []Point) error {
	if minimumSpeed < 1 || minimumSpeed > 100 {
		return errors.New("minimum speed must be between 1 and 100")
	}
	if mode == ModeFixed {
		if fixedSpeed < minimumSpeed || fixedSpeed > 100 {
			return fmt.Errorf("fixed speed must be between %d and 100", minimumSpeed)
		}
		return nil
	}
	if mode != ModeCurve {
		return fmt.Errorf("unknown control mode %q", mode)
	}
	if len(points) < 2 {
		return errors.New("curve requires at least two points")
	}
	for index, point := range points {
		if point.Temperature < 0 || point.Temperature > 120 {
			return fmt.Errorf("curve point %d temperature must be between 0 and 120", index)
		}
		if point.Speed < minimumSpeed || point.Speed > 100 {
			return fmt.Errorf("curve point %d speed must be between %d and 100", index, minimumSpeed)
		}
		if index > 0 {
			if point.Temperature <= points[index-1].Temperature {
				return errors.New("curve temperatures must be strictly increasing")
			}
			if point.Speed < points[index-1].Speed {
				return errors.New("curve speeds must not decrease")
			}
		}
	}
	return nil
}

func SpeedForTemperature(points []Point, temperature float64) int {
	if len(points) == 0 {
		return 100
	}
	ordered := append([]Point(nil), points...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Temperature < ordered[right].Temperature
	})
	if temperature <= ordered[0].Temperature {
		return ordered[0].Speed
	}
	for index := 1; index < len(ordered); index++ {
		upper := ordered[index]
		if temperature <= upper.Temperature {
			lower := ordered[index-1]
			ratio := (temperature - lower.Temperature) / (upper.Temperature - lower.Temperature)
			return int(math.Round(float64(lower.Speed) + ratio*float64(upper.Speed-lower.Speed)))
		}
	}
	return ordered[len(ordered)-1].Speed
}
