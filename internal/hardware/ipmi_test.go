package hardware

import "testing"

func TestParseFanSensors(t *testing.T) {
	output := `CPU_FAN1        | 1200.000   | RPM        | ok
CPU_FAN2        | na         | RPM        | na
SYS_FAN1        | 840.000    | RPM        | ok
12V             | 12.096     | Volts      | ok
FANA            | 900.000    | RPM        | ok`

	readings := parseFanSensors(output)
	if len(readings) != 3 {
		t.Fatalf("readings count = %d, want 3", len(readings))
	}
	if readings[0].Name != "CPU_FAN1" || readings[0].Group != "cpu" || readings[0].RPM != 1200 {
		t.Fatalf("unexpected CPU reading: %#v", readings[0])
	}
	if readings[1].Name != "SYS_FAN1" || readings[1].Group != "system" || readings[1].RPM != 840 {
		t.Fatalf("unexpected system reading: %#v", readings[1])
	}
	if readings[2].Group != "other" || readings[2].RPM != 900 {
		t.Fatalf("unexpected generic reading: %#v", readings[2])
	}
}
