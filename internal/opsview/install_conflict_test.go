package opsview_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestInstallConflict_MakesTheSweepUnclean(t *testing.T) {
	r := opsview.DoctorReport{
		InstallConflict: &opsview.DoctorInstallConflict{
			Self:      "/a/sparkwing",
			Competing: []opsview.DoctorInstallCopy{{Path: "/b/sparkwing"}},
		},
	}
	if r.Clean() {
		t.Fatal("a report naming competing installs called itself clean")
	}
}

func TestRenderDoctor_ExplainsCompetingInstalls(t *testing.T) {
	r := opsview.DoctorReport{
		InstallConflict: &opsview.DoctorInstallConflict{
			Self: "/home/u/.local/bin/sparkwing",
			Competing: []opsview.DoctorInstallCopy{
				{Path: "/home/u/go/bin/sparkwing", Modified: "2026-08-02T00:50:00Z"},
			},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "pretty", ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"this binary: /home/u/.local/bin/sparkwing",
		"/home/u/go/bin/sparkwing",
		"2026-08-02T00:50:00Z",
		installsite.RetireRemedy("/home/u/go/bin/sparkwing").Text(),
		"PATH",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"canonical", "upgrade", "downgrade", "older", "newer"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("doctor output claims %q without measuring it:\n%s", forbidden, out)
		}
	}

	var plain bytes.Buffer
	if err := opsview.RenderDoctor(&plain, r, "plain", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.String(), "competing_installs\t1") {
		t.Errorf("plain output missing the count:\n%s", plain.String())
	}
}

func TestRenderDoctor_QuotesRemedyPathsWithSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the POSIX-quoted form")
	}
	r := opsview.DoctorReport{
		InstallConflict: &opsview.DoctorInstallConflict{
			Self: "/home/u/.local/bin/sparkwing",
			Competing: []opsview.DoctorInstallCopy{
				{Path: "/home/u/My Apps/sparkwing", Modified: "2026-08-02T00:50:00Z"},
			},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "pretty", ""); err != nil {
		t.Fatal(err)
	}
	want := "test ! -e '/home/u/My Apps/sparkwing.superseded' && test ! -L '/home/u/My Apps/sparkwing.superseded' && mv -n -- '/home/u/My Apps/sparkwing' '/home/u/My Apps/sparkwing.superseded' && test ! -e '/home/u/My Apps/sparkwing' && test ! -L '/home/u/My Apps/sparkwing'"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("doctor remedy is not paste-safe; want %q in:\n%s", want, buf.String())
	}
}
