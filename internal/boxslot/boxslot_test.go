package boxslot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedStale(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("seed stale marker: %v", err)
	}
}

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

func TestPurgeIfIdleInRootStaysWithRenamedDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "box-slots")
	seedStale(t, dir, "holder-pid99999-1700000000000000000-1.lock", "run=inside\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	renamed := filepath.Join(parent, "box-slots-original")
	if err := os.Rename(dir, renamed); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "holder-pid88888-1800000000000000000-1.lock")
	if err := os.WriteFile(victim, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}

	removed, live, err := PurgeIfIdleInRoot(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(live) != 0 {
		t.Fatalf("purge = removed %d, live %+v", removed, live)
	}
	if _, err := os.Stat(filepath.Join(renamed, "holder-pid99999-1700000000000000000-1.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned stale marker remains: %v", err)
	}
	body, err := os.ReadFile(victim)
	if err != nil || string(body) != "outside" {
		t.Fatalf("outside marker changed: body=%q err=%v", body, err)
	}
}

func TestPurgeIfIdleInRootRevalidatesHolderLockedAfterScan(t *testing.T) {
	dir := t.TempDir()
	name := "holder-pid99999-1700000000000000000-1.lock"
	seedStale(t, dir, name, "run=late-lock\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	holders, err := HoldersInRoot(root, dir)
	if err != nil || len(holders) != 1 || holders[0].Live {
		t.Fatalf("initial holders = %+v, err=%v", holders, err)
	}
	locked, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := flockExclusive(locked); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = flockUnlock(locked); _ = locked.Close() }()

	removed, live, err := PurgeIfIdleInRoot(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || len(live) != 1 || !live[0].Live {
		t.Fatalf("purge = removed %d, live %+v", removed, live)
	}
	if _, err := root.Stat(name); err != nil {
		t.Fatalf("newly locked holder was removed: %v", err)
	}
}

func TestPurgeIfIdleInRootReturnsPromptlyWhenCoordinationIsBusy(t *testing.T) {
	dir := t.TempDir()
	stale := "holder-pid99999-1700000000000000000-1.lock"
	seedStale(t, dir, stale, "run=stale\n")
	coordPath := filepath.Join(dir, "coord.lock")
	coord, err := os.OpenFile(coordPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := flockExclusive(coord); err != nil {
		t.Fatal(err)
	}
	coordLocked := true
	defer func() {
		if coordLocked {
			_ = flockUnlock(coord)
		}
		_ = coord.Close()
	}()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	type fileSnapshot struct {
		body string
		mode os.FileMode
	}
	paths := []string{coordPath, filepath.Join(dir, stale)}
	before := make(map[string]fileSnapshot, len(paths))
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s before purge: %v", path, readErr)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s before purge: %v", path, statErr)
		}
		before[path] = fileSnapshot{body: string(body), mode: info.Mode()}
	}

	type purgeResult struct {
		removed int
		live    []Holder
		err     error
	}
	resultCh := make(chan purgeResult, 1)
	go func() {
		removed, live, purgeErr := PurgeIfIdleInRoot(root, dir)
		resultCh <- purgeResult{removed: removed, live: live, err: purgeErr}
	}()

	var result purgeResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		if unlockErr := flockUnlock(coord); unlockErr != nil {
			t.Fatalf("unlock coordination after blocked purge: %v", unlockErr)
		}
		coordLocked = false
		select {
		case <-resultCh:
			t.Fatal("busy coordination blocked purge")
		case <-time.After(time.Second):
			t.Fatal("purge remained blocked after releasing coordination lock")
		}
	}
	if !errors.Is(result.err, errCoordBusy) {
		t.Fatalf("purge error = %v, want coordination-busy error", result.err)
	}
	if result.removed != 0 || len(result.live) != 0 {
		t.Fatalf("busy purge = removed %d, live %+v", result.removed, result.live)
	}
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("busy purge changed %s: %v", path, readErr)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("busy purge changed %s: %v", path, statErr)
		}
		if got, want := string(body), before[path].body; got != want {
			t.Errorf("busy purge changed %s contents: got %q, want %q", path, got, want)
		}
		if got, want := info.Mode(), before[path].mode; got != want {
			t.Errorf("busy purge changed %s mode: got %s, want %s", path, got, want)
		}
	}
}

func TestPurgeIfIdleInRootSerializesConcurrentLegacyCreator(t *testing.T) {
	dir := t.TempDir()
	seedStale(t, dir, "holder-pid99999-1700000000000000000-1.lock", "run=stale\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	type creatorResult struct {
		holder *os.File
		err    error
	}
	started := make(chan struct{})
	created := make(chan creatorResult, 1)
	removed, live, err := purgeIfIdleInRoot(root, dir, func() {
		go func() {
			coord, openErr := os.OpenFile(filepath.Join(dir, "coord.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			close(started)
			if openErr != nil {
				created <- creatorResult{err: openErr}
				return
			}
			if lockErr := flockExclusive(coord); lockErr != nil {
				_ = coord.Close()
				created <- creatorResult{err: lockErr}
				return
			}
			holder, createErr := os.OpenFile(
				filepath.Join(dir, "holder-pid4242-1800000000000000000-1.lock"),
				os.O_CREATE|os.O_EXCL|os.O_RDWR,
				0o600,
			)
			if createErr == nil {
				createErr = flockExclusive(holder)
			}
			_ = flockUnlock(coord)
			_ = coord.Close()
			created <- creatorResult{holder: holder, err: createErr}
		}()
		<-started
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(live) != 0 {
		t.Fatalf("purge = removed %d, live %+v", removed, live)
	}
	result := <-created
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer func() { _ = flockUnlock(result.holder); _ = result.holder.Close() }()
	holders, err := HoldersInRoot(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(holders) != 1 || !holders[0].Live || holders[0].PID != 4242 {
		t.Fatalf("concurrent creator holder = %+v", holders)
	}
}

func TestPurgeIfIdle_ClearsStaleFilesAndRetainsCoordination(t *testing.T) {
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
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "coord.lock")); err != nil {
		t.Fatalf("coordination lock did not survive purge: %v", err)
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
