package main

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// A fake systemctl that reports the given User= for roadie.service.
func fakeSystemctl(t *testing.T, serviceUser string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + serviceUser + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestServiceAccountHint(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	fakeSystemctl(t, "someone-else")
	if code := serviceAccountHint("token"); code != 1 {
		t.Fatalf("another account runs the service: got %d, want 1", code)
	}
	fakeSystemctl(t, me.Username)
	if code := serviceAccountHint("token"); code != 0 {
		t.Fatalf("the service runs as us: got %d, want 0", code)
	}
	fakeSystemctl(t, "")
	if code := serviceAccountHint("token"); code != 0 {
		t.Fatalf("no service: got %d, want 0", code)
	}
	t.Setenv("PATH", t.TempDir()) // no systemctl at all
	if code := serviceAccountHint("token"); code != 0 {
		t.Fatalf("no systemd: got %d, want 0", code)
	}
}
