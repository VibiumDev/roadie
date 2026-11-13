package devices

import "testing"

func TestHeatString(t *testing.T) {
	for _, c := range []struct {
		h    Heat
		want string
	}{
		{Heat{TempC: 64.25, readTemp: true, ClockMHz: 1500, MaxClockMHz: 1800, Load1: 3.2, load: true}, "SoC 64.2 °C, CPU 1500 of 1800 MHz, load 3.20"},
		{Heat{Load1: 0.5, load: true}, "load 0.50"},
		{Heat{ClockMHz: 1500}, ""}, // a clock with no ceiling says nothing
		{Heat{}, ""},
	} {
		if got := c.h.String(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.h, got, c.want)
		}
	}
	// On this machine, whatever it can read must not panic.
	_ = ReadHeat().String()
}
