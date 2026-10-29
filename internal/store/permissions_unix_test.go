//go:build !windows

package store_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
	st, err := teststore.OpenExisting(path)
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

func TestOpenTightensExistingSQLiteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := teststore.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if err := teststore.ForTeam(t, first, store.DefaultTeam).TestOnlySeedRun(t.Context(), store.Run{RunSummary: runstate.RunSummary{ID: "run-before", Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	files := []string{path, path + "-wal", path + "-shm"}
	for _, name := range files {
		if err := os.Chmod(name, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	second, err := teststore.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	for _, name := range files {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o after reopening, want 0600", name, got)
		}
	}
	if _, err := teststore.ForTeam(t, second, store.DefaultTeam).GetRun(t.Context(), "run-before"); err != nil {
		t.Fatalf("reopened store lost its run: %v", err)
	}
}
