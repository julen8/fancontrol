package control

import "testing"

func TestSpeedForTemperature(t *testing.T) {
	points := []Point{{Temperature: 35, Speed: 40}, {Temperature: 50, Speed: 60}, {Temperature: 65, Speed: 100}}
	tests := []struct {
		name        string
		temperature float64
		want        int
	}{
		{name: "below curve", temperature: 20, want: 40},
		{name: "at point", temperature: 50, want: 60},
		{name: "interpolated", temperature: 57.5, want: 80},
		{name: "above curve", temperature: 80, want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SpeedForTemperature(points, test.temperature); got != test.want {
				t.Fatalf("SpeedForTemperature() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestValidateRejectsDescendingSpeed(t *testing.T) {
	points := []Point{{Temperature: 35, Speed: 60}, {Temperature: 50, Speed: 40}}
	if err := Validate(ModeCurve, 50, 20, points); err == nil {
		t.Fatal("Validate() accepted a descending curve")
	}
}
