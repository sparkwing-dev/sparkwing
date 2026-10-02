//go:build !windows

package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestReopeningStorePreservesLiveSQLiteFiles(t *testing.T) {
	if path := os.Getenv("SPARKWING_STORE_LOCK_HELPER"); path != "" {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateRun(t.Context(), store.Run{ID: "other-process", Pipeline: "sample", Status: "running"}); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, mode := range []os.FileMode{0o600, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			first, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := first.Close(); err != nil {
					t.Error(err)
				}
			})
			reader, err := store.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := reader.CurrentSchemaVersion(t.Context()); err != nil {
				t.Fatal(err)
			}
			files := map[string]os.FileInfo{}
			for _, name := range []string{path, path + "-wal", path + "-shm"} {
				if err := os.Chmod(name, mode); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(name)
				if err != nil {
					t.Fatal(err)
				}
				files[name] = info
			}
			second, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := second.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReopeningStorePreservesLiveSQLiteFiles$")
			cmd.Env = append(os.Environ(), "SPARKWING_STORE_LOCK_HELPER="+path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("external writer: %v\n%s", err, out)
			}
			for name, before := range files {
				after, err := os.Stat(name)
				if err != nil {
					t.Fatalf("live SQLite file removed: %s: %v", name, err)
				}
				if !os.SameFile(before, after) {
					t.Fatalf("live SQLite file replaced: %s", name)
				}
			}
			if _, err := reader.GetRun(t.Context(), "other-process"); err != nil {
				t.Fatalf("existing reader: %v", err)
			}
		})
	}
}
