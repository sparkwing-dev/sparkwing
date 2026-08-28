//go:build !windows

package store_test

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOpenCreatesPrivateSQLiteFiles(t *testing.T) {
	if os.Getenv("SPARKWING_STORE_UMASK_HELPER") != "1" {
		root := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenCreatesPrivateSQLiteFiles$")
		cmd.Env = append(os.Environ(),
			"SPARKWING_STORE_UMASK_HELPER=1",
			"SPARKWING_STORE_TEST_ROOT="+root,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("umask helper: %v\n%s", err, out)
		}
		return
	}

	syscall.Umask(0)
	path := filepath.Join(os.Getenv("SPARKWING_STORE_TEST_ROOT"), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", candidate, got)
		}
	}
}

func TestOpenReadOnlyDoesNotCreateOrTouchSQLiteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnlySnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ro.DB().QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil {
		_ = ro.Close()
		t.Fatal(err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("read-only open changed state.db: before=%+v after=%+v", before, after)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
			t.Fatalf("read-only open created %s: %v", sidecar, err)
		}
	}
}

func TestOpenReadOnlySnapshotIncludesLiveWALWithoutTouchingSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := writer.CreateRun(t.Context(), store.Run{
		ID: "run-in-wal", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("live WAL is not populated: info=%v err=%v", info, err)
	}

	immutable, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	var immutableCount int
	if err := immutable.QueryRow(`SELECT COUNT(*) FROM runs WHERE id = 'run-in-wal'`).Scan(&immutableCount); err != nil {
		_ = immutable.Close()
		t.Fatal(err)
	}
	if err := immutable.Close(); err != nil {
		t.Fatal(err)
	}
	if immutableCount != 0 {
		t.Fatalf("negative control saw WAL-only row through immutable source: count=%d", immutableCount)
	}

	before := snapshotSQLiteSource(t, path)
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	snapshot, err := store.OpenReadOnlySnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshotCount int
	if err := snapshot.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE id = 'run-in-wal'`).Scan(&snapshotCount); err != nil {
		_ = snapshot.Close()
		t.Fatal(err)
	}
	if snapshotCount != 1 {
		_ = snapshot.Close()
		t.Fatalf("snapshot count = %d, want live WAL row", snapshotCount)
	}
	after := snapshotSQLiteSource(t, path)
	for _, candidate := range []string{path, path + "-wal"} {
		if after[candidate] != before[candidate] {
			_ = snapshot.Close()
			t.Fatalf("snapshot changed %s: before=%+v after=%+v", candidate, before[candidate], after[candidate])
		}
	}
	if after[path+"-shm"].Mode != before[path+"-shm"].Mode || after[path+"-shm"].Size != before[path+"-shm"].Size {
		_ = snapshot.Close()
		t.Fatalf("snapshot changed SHM shape: before=%+v after=%+v", before[path+"-shm"], after[path+"-shm"])
	}
	if matches, err := filepath.Glob(filepath.Join(tempRoot, "sparkwing-store-snapshot-*")); err != nil || len(matches) != 1 {
		_ = snapshot.Close()
		t.Fatalf("snapshot temp directories = %v, err=%v", matches, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if matches, err := filepath.Glob(filepath.Join(tempRoot, "sparkwing-store-snapshot-*")); err != nil || len(matches) != 0 {
		t.Fatalf("snapshot temp directories after close = %v, err=%v", matches, err)
	}
}

func TestOpenReadOnlySnapshotDoesNotCreateMissingSourceSHM(t *testing.T) {
	livePath := filepath.Join(t.TempDir(), "live.db")
	writer, err := store.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := writer.CreateRun(t.Context(), store.Run{
		ID: "run-in-wal", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	sourcePath := filepath.Join(t.TempDir(), "state.db")
	copySQLiteTestFile(t, livePath, sourcePath)
	copySQLiteTestFile(t, livePath+"-wal", sourcePath+"-wal")
	if _, err := os.Lstat(sourcePath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("source SHM exists before snapshot: %v", err)
	}
	before := snapshotSQLitePair(t, sourcePath)

	snapshot, err := store.OpenReadOnlySnapshot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	var count int
	if err := snapshot.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE id = 'run-in-wal'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("snapshot count = %d, want WAL-only row", count)
	}
	if _, err := os.Lstat(sourcePath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("snapshot created source SHM: %v", err)
	}
	if after := snapshotSQLitePair(t, sourcePath); !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot changed source pair\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestOpenReadOnlySnapshotReportsStableCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.OpenReadOnlySnapshot(path)
	if err == nil {
		t.Fatal("snapshot of corrupt database succeeded")
	}
	if strings.Contains(err.Error(), "kept changing") {
		t.Fatalf("stable corruption was mislabeled as churn: %v", err)
	}
	if !strings.Contains(err.Error(), "validate sqlite snapshot") {
		t.Fatalf("corruption error lost snapshot validation context: %v", err)
	}
	if !strings.Contains(err.Error(), "file is not a database") {
		t.Fatalf("corruption error lost SQLite's hard cause: %v", err)
	}
}

func TestOpenReadOnlySnapshotReportsUnreadableWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	walPath := path + "-wal"
	if err := os.WriteFile(walPath, []byte("unreadable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(walPath, 0); err != nil {
		t.Fatal(err)
	}
	if probe, probeErr := os.Open(walPath); probeErr == nil {
		_ = probe.Close()
		t.Skip("test process can read mode-000 files")
	}
	_, err = store.OpenReadOnlySnapshot(path)
	if err == nil {
		t.Fatal("snapshot with unreadable WAL succeeded")
	}
	if strings.Contains(err.Error(), "kept changing") {
		t.Fatalf("stable unreadable WAL was mislabeled as churn: %v", err)
	}
	if !strings.Contains(err.Error(), "open sqlite snapshot source "+walPath) {
		t.Fatalf("unreadable WAL error lost its hard cause: %v", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unreadable WAL error lost permission cause: %v", err)
	}
}

func copySQLiteTestFile(t *testing.T, source, destination string) {
	t.Helper()
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotSQLitePair(t *testing.T, path string) map[string]sqliteSourceState {
	t.Helper()
	state := map[string]sqliteSourceState{}
	for _, candidate := range []string{path, path + "-wal"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		state[candidate] = sqliteSourceState{
			Mode: info.Mode(), Size: info.Size(), Digest: sha256.Sum256(body),
		}
	}
	return state
}

type sqliteSourceState struct {
	Mode   os.FileMode
	Size   int64
	Digest [sha256.Size]byte
}

func snapshotSQLiteSource(t *testing.T, path string) map[string]sqliteSourceState {
	t.Helper()
	state := map[string]sqliteSourceState{}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		state[candidate] = sqliteSourceState{
			Mode: info.Mode(), Size: info.Size(), Digest: sha256.Sum256(body),
		}
	}
	return state
}
