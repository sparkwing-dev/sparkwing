package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// otherInstall writes a stand-in for a second sparkwing binary that
// exists but is not the one running this test.
func otherInstall(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), installsite.ExeName())
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestVersionMemoryIsScopedToTheInstall is the BW-1675 regression. Two
// installs on one machine used to take turns stamping one shared
// last-version file with their own version, so the record read as an
// upgrade every time the other binary ran and the evidence described
// whichever process looked at it last. Each install now keeps its own
// stamp: seeding one install's history, announcing its transition, and
// re-stamping must never touch or be touched by the other's, and
// alternating invocations must go quiet once each install has announced
// its own transition -- the shared record re-announced forever.
func TestVersionMemoryIsScopedToTheInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	pendingUpgradeNotice = ""
	p := paths.PathsAt(home)

	exeA := otherInstall(t)
	exeB := otherInstall(t)
	stampA := p.VersionStampFile(installsite.PathKey(exeA))
	stampB := p.VersionStampFile(installsite.PathKey(exeB))
	if stampA == stampB {
		t.Fatalf("two installs share a stamp file: %s", stampA)
	}
	writeVersionStamp(stampA, exeA, "v0.14.0")
	writeVersionStamp(stampB, exeB, "v0.20.0")

	var a bytes.Buffer
	noteVersionTransitionForExe(&a, "version", p, exeA)
	if !strings.Contains(a.String(), "sparkwing changed v0.14.0 -> ") {
		t.Fatalf("install A lost its own transition; got %q", a.String())
	}
	if got := readVersionStamp(stampB); got != "v0.20.0" {
		t.Fatalf("install A's run rewrote install B's memory: %q", got)
	}

	var b bytes.Buffer
	noteVersionTransitionForExe(&b, "version", p, exeB)
	if !strings.Contains(b.String(), "sparkwing changed v0.20.0 -> ") {
		t.Fatalf("install B lost its own transition; got %q", b.String())
	}

	for i := range 4 {
		var buf bytes.Buffer
		exe := exeA
		if i%2 == 1 {
			exe = exeB
		}
		noteVersionTransitionForExe(&buf, "version", p, exe)
		if buf.Len() != 0 {
			t.Fatalf("alternating run %d re-announced a transition: %q", i, buf.String())
		}
	}
}

// TestVersionMemorySurvivesMultipleHomes keeps the per-install scoping
// from collapsing homes together: the same install under two
// SPARKWING_HOMEs stamps each home separately.
func TestVersionMemorySurvivesMultipleHomes(t *testing.T) {
	exe := otherInstall(t)
	pendingUpgradeNotice = ""
	pA := paths.PathsAt(t.TempDir())
	pB := paths.PathsAt(t.TempDir())

	var buf bytes.Buffer
	noteVersionTransitionForExe(&buf, "version", pA, exe)
	if got := readVersionStamp(pA.VersionStampFile(installsite.PathKey(exe))); got != installedVersion() {
		t.Fatalf("home A stamp = %q, want %q", got, installedVersion())
	}
	if got := readVersionStamp(pB.VersionStampFile(installsite.PathKey(exe))); got != "" {
		t.Fatalf("home B was stamped by a run against home A: %q", got)
	}
}

// TestInstallClaimVerbIsGone pins the removal of the hidden
// `_install-claim` verb, which could rename binaries across the
// machine. Detection is read-only now; a reintroduced mutating verb
// should have to fight this test.
func TestInstallClaimVerbIsGone(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	err := runSparkwing([]string{"_install-claim"})
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("_install-claim dispatched; err = %v", err)
	}
}
