package boxslot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// seedStale writes a holder marker whose flock nobody holds, so it reads
// as a dead owner's leftover.
func seedStale(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("seed stale marker: %v", err)
	}
}

// seedLive creates a holder marker and holds its exclusive flock for the
// rest of the test, so it reads as a live owner.
func seedLive(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open live marker: %v", err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("write live marker: %v", err)
	}
	if err := flockExclusive(f); err != nil {
		t.Fatalf("flock live marker: %v", err)
	}
	t.Cleanup(func() { _ = flockUnlock(f); _ = f.Close() })
}

func TestHolders_ReportsLiveAndStaleWithParsedMetadata(t *testing.T) {
	dir := t.TempDir()
	seedStale(t, dir, "holder-pid99999-1700000000000000000-1.lock",
		"pid=99999 start=2026-01-01T00:00:00Z\nrun=run-20260101-000000-cafe0001\n")
	seedLive(t, dir, "holder-pid4242-1800000000000000000-1.lock",
		"pid=4242 start=2026-02-02T00:00:00Z\nrun=run-20260202-000000-beef0002\n")

	holders, err := Holders(dir)
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}
	if len(holders) != 2 {
		t.Fatalf("Holders returned %d rows, want 2: %+v", len(holders), holders)
	}

	stale := holders[0]
	if stale.PID != 99999 || stale.Live {
		t.Errorf("stale row = %+v, want pid 99999 and Live=false", stale)
	}
	if stale.RunID != "run-20260101-000000-cafe0001" {
		t.Errorf("stale RunID = %q", stale.RunID)
	}
	if got := stale.ClaimedAt.UnixNano(); got != 1700000000000000000 {
		t.Errorf("stale ClaimedAt = %d", got)
	}

	live := holders[1]
	if live.PID != 4242 || !live.Live {
		t.Errorf("live row = %+v, want pid 4242 and Live=true", live)
	}
	if live.RunID != "run-20260202-000000-beef0002" {
		t.Errorf("live RunID = %q", live.RunID)
	}
}

func TestHolders_AbsentLockDirReportsNone(t *testing.T) {
	holders, err := Holders(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("Holders on absent dir: %v", err)
	}
	if len(holders) != 0 {
		t.Fatalf("Holders on absent dir = %+v, want none", holders)
	}
}

func TestPurgeIfIdle_RemovesEverythingWhenNoLiveHolder(t *testing.T) {
	dir := t.TempDir()
	seedStale(t, dir, "holder-pid99999-1700000000000000000-1.lock", "pid=99999\n")
	seedStale(t, dir, "waiter-pid88888-1700000000000000000-1.lock", "")
	seedStale(t, dir, "coord.lock", "")
	seedStale(t, dir, "cap.control", "3\n")

	removed, live, err := PurgeIfIdle(dir)
	if err != nil {
		t.Fatalf("PurgeIfIdle: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("PurgeIfIdle reported live holders on an idle dir: %+v", live)
	}
	if removed != 4 {
		t.Fatalf("removed = %d, want 4", removed)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle box-slot dir survived purge: %v", err)
	}
}

func TestPurgeIfIdle_ReportsLiveHolderWithoutDeleting(t *testing.T) {
	dir := t.TempDir()
	seedStale(t, dir, "holder-pid99999-1700000000000000000-1.lock", "pid=99999\n")
	liveName := "holder-pid4242-1800000000000000000-1.lock"
	seedLive(t, dir, liveName, "pid=4242\nrun=run-20260202-000000-beef0002\n")

	removed, live, err := PurgeIfIdle(dir)
	if err != nil {
		t.Fatalf("PurgeIfIdle: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 while a holder is live", removed)
	}
	if len(live) != 1 || live[0].PID != 4242 {
		t.Fatalf("live = %+v, want one row for pid 4242", live)
	}
	if _, err := os.Stat(filepath.Join(dir, liveName)); err != nil {
		t.Fatalf("live holder marker was removed: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("box-slot dir removed while a holder is live: %v", err)
	}
}

func TestPurgeIfIdle_AbsentDirIsNoOp(t *testing.T) {
	removed, live, err := PurgeIfIdle(filepath.Join(t.TempDir(), "never-created"))
	if err != nil || removed != 0 || live != nil {
		t.Fatalf("PurgeIfIdle absent dir = (%d, %+v, %v), want (0, nil, nil)", removed, live, err)
	}
}
