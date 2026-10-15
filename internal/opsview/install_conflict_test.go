package opsview_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
)

func writeInstall(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, installsite.ExeName())
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallConflict_SilentOnASingleInstall(t *testing.T) {
	dir := t.TempDir()
	self := writeInstall(t, filepath.Join(dir, "local", "bin"))

	if c := opsview.InstallConflict(self, []string{filepath.Dir(self)}); c != nil {
		t.Fatalf("one install reported a conflict: %+v", c)
	}
}

func TestInstallConflict_ReportsEveryOtherInstall(t *testing.T) {
	dir := t.TempDir()
	self := writeInstall(t, filepath.Join(dir, "local", "bin"))
	stale := writeInstall(t, filepath.Join(dir, "go", "bin"))

	c := opsview.InstallConflict(self, []string{filepath.Dir(self), filepath.Dir(stale)})
	if c == nil {
		t.Fatal("a second install went unreported")
	}
	if c.Self != self {
		t.Errorf("self = %q, want %q", c.Self, self)
	}
	if len(c.Competing) != 1 || c.Competing[0].Path != stale {
		t.Fatalf("competing = %+v, want just %s", c.Competing, stale)
	}
}

func TestInstallConflict_SeesInstallsOffTheCallersPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the well-known POSIX install dirs do not apply")
	}
	home := t.TempDir()
	self := writeInstall(t, filepath.Join(home, "shellbin"))
	rival := writeInstall(t, filepath.Join(home, ".local", "bin"))

	env := map[string]string{"PATH": filepath.Dir(self)}
	dirs := installsite.SearchDirs(func(k string) string { return env[k] }, home)

	c := opsview.InstallConflict(self, dirs)
	if c == nil {
		t.Fatal("an install off the caller's PATH went unreported")
	}
	if len(c.Competing) != 1 || c.Competing[0].Path != rival {
		t.Fatalf("competing = %+v, want just %s", c.Competing, rival)
	}
}
