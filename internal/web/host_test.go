package web

import "testing"

func TestParseMeminfo(t *testing.T) {
	total, used := parseMeminfo("MemTotal:        7996724 kB\nMemFree:          401088 kB\nMemAvailable:    5997543 kB\n")
	if total == nil || *total != 7809 {
		t.Errorf("total = %v, want 7809 MB", total)
	}
	if used == nil || *round1(used) != 25 {
		t.Errorf("used = %v, want 25%%", used)
	}
	if total, used := parseMeminfo("MemTotal: 1024 kB\n"); total == nil || *total != 1 || used != nil {
		t.Errorf("without MemAvailable: total %v, used %v", total, used)
	}
	if total, used := parseMeminfo(""); total != nil || used != nil {
		t.Errorf("nothing: total %v, used %v", total, used)
	}
}
