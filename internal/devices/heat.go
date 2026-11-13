package devices

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Heat is how hot and how hard the host is running, read from Linux's
// files. A field is zero where the file is missing, as on macOS.
type Heat struct {
	TempC          float64 // the SoC's temperature
	ClockMHz       int     // the first core's clock now
	MaxClockMHz    int     // and its ceiling; below it means throttled
	Load1          float64
	readTemp, load bool
}

// ReadHeat reads the host's temperature, CPU clock and load.
func ReadHeat() Heat {
	var h Heat
	if v, ok := readNumber("/sys/class/thermal/thermal_zone0/temp"); ok {
		h.TempC, h.readTemp = v/1000, true
	}
	if v, ok := readNumber("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"); ok {
		h.ClockMHz = int(v / 1000)
	}
	if v, ok := readNumber("/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq"); ok {
		h.MaxClockMHz = int(v / 1000)
	}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				h.Load1, h.load = v, true
			}
		}
	}
	return h
}

// String is the reading for a log line, such as "SoC 64.3 °C, CPU 1800 of
// 1800 MHz, load 0.80", or "" where nothing could be read.
func (h Heat) String() string {
	var parts []string
	if h.readTemp {
		parts = append(parts, fmt.Sprintf("SoC %.1f °C", h.TempC))
	}
	if h.ClockMHz > 0 && h.MaxClockMHz > 0 {
		parts = append(parts, fmt.Sprintf("CPU %d of %d MHz", h.ClockMHz, h.MaxClockMHz))
	}
	if h.load {
		parts = append(parts, fmt.Sprintf("load %.2f", h.Load1))
	}
	return strings.Join(parts, ", ")
}

// LogHeat logs the host's heat every interval until ctx ends, so a capture
// failure's own reading can be compared with the hours around it.
func LogHeat(ctx context.Context, every time.Duration) {
	if ReadHeat().String() == "" {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			log.Printf("host: %s", ReadHeat())
		}
	}
}

func readNumber(path string) (float64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	return v, err == nil
}
