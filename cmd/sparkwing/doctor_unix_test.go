//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// holdFlock creates a marker file and holds an exclusive flock on it for
// the rest of the test, so a probe reads its owner as live.
func holdFlock(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock %s: %v", path, err)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() })
}

func TestDiagnose_ReportsLiveLegacyHolderWithoutDeleting(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	boxDir := p.BoxSlotDir()
	if err := os.MkdirAll(boxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleName := "holder-pid99999-1700000000000000000-1.lock"
	if err := os.WriteFile(filepath.Join(boxDir, staleName), []byte("pid=99999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveName := "holder-pid4242-1800000000000000000-1.lock"
	holdFlock(t, filepath.Join(boxDir, liveName), "pid=4242\nrun=run-legacy\n")

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.LiveLegacyHolders) != 1 || rep.LiveLegacyHolders[0].PID != 4242 {
		t.Fatalf("LiveLegacyHolders = %+v, want one row for pid 4242", rep.LiveLegacyHolders)
	}
	if rep.LegacyBoxSlotFilesRemoved != 0 {
		t.Fatalf("removed %d files while a holder is live, want 0", rep.LegacyBoxSlotFilesRemoved)
	}
	if _, err := os.Stat(filepath.Join(boxDir, liveName)); err != nil {
		t.Fatalf("live holder marker removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(boxDir, staleName)); err != nil {
		t.Fatalf("stale marker removed while a holder is live: %v", err)
	}
}

func TestDiagnose_PurgesIdleLegacyBoxSlots(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	boxDir := p.BoxSlotDir()
	if err := os.MkdirAll(boxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"holder-pid99999-1700000000000000000-1.lock", "coord.lock", "cap.control"} {
		if err := os.WriteFile(filepath.Join(boxDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if rep.LegacyBoxSlotFilesRemoved != 3 {
		t.Fatalf("LegacyBoxSlotFilesRemoved = %d, want 3", rep.LegacyBoxSlotFilesRemoved)
	}
	if _, err := os.Stat(boxDir); !os.IsNotExist(err) {
		t.Fatalf("idle box-slot dir survived: %v", err)
	}
}
