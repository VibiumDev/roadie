package main

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Host metrics for /health: how hard this machine is working and how hot it
// is getting. A Pi encoding 1080p30 sits near a full core, and a Pi near its
// thermal limit drops its clocks -- which shows up as input and video that
// are intermittently late, with nothing in the logs to explain it.
//
// Everything here is read from files and degrades to "absent" rather than
// failing, so a platform without them simply reports less. /health is polled
// once a second by every open viewer, so nothing here may be expensive.
type hostStats struct {
	CPUPercent *float64 `json:"cpu_percent,omitempty"` // this process, across all cores
	Load1      *float64 `json:"load1,omitempty"`
	TempC      *float64 `json:"temp_c,omitempty"`
}

const clockTicksPerSecond = 100 // _SC_CLK_TCK, 100 on every Linux worth the name

var cpuSample struct {
	sync.Mutex
	ticks   float64
	at      time.Time
	percent float64
	valid   bool
}

// processCPUPercent returns this process's CPU use since the previous call.
// The first call has nothing to compare against and reports nothing.
func processCPUPercent() *float64 {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return nil
	}
	// Fields after the comm field, which may itself contain spaces.
	close := strings.LastIndex(string(raw), ")")
	if close < 0 {
		return nil
	}
	fields := strings.Fields(string(raw)[close+1:])
	// utime and stime are fields 14 and 15 of the full line; the slice above
	// starts at field 3, so they are at indices 11 and 12.
	if len(fields) < 13 {
		return nil
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return nil
	}

	now := time.Now()
	cpuSample.Lock()
	defer cpuSample.Unlock()
	ticks := utime + stime
	if !cpuSample.valid {
		cpuSample.ticks, cpuSample.at, cpuSample.valid = ticks, now, true
		return nil
	}
	elapsed := now.Sub(cpuSample.at).Seconds()
	if elapsed < 0.2 {
		// Too soon to measure again; report what the last interval showed.
		p := cpuSample.percent
		return &p
	}
	used := (ticks - cpuSample.ticks) / clockTicksPerSecond
	cpuSample.ticks, cpuSample.at = ticks, now
	cpuSample.percent = used / elapsed * 100
	p := cpuSample.percent
	return &p
}

func loadAverage1() *float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil
	}
	return &v
}

func cpuTemperature() *float64 {
	raw, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return nil
	}
	milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return nil
	}
	c := milli / 1000
	return &c
}

// round1 keeps these readable: a tenth is as much precision as any of
// these numbers deserves.
func round1(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*10) / 10
	return &r
}

// readHostStats returns nil when the machine tells us nothing, so /health
// omits the field entirely rather than carrying an empty object.
func readHostStats() *hostStats {
	h := hostStats{
		CPUPercent: round1(processCPUPercent()),
		Load1:      loadAverage1(),
		TempC:      round1(cpuTemperature()),
	}
	if h.CPUPercent == nil && h.Load1 == nil && h.TempC == nil {
		return nil
	}
	return &h
}
