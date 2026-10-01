package teststore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOpenKeepsStateAndIdentitySeparate(t *testing.T) {
	ctx := context.Background()
	var fixtures [2]*store.Store
	var authorities [2]string
	for i := range fixtures {
		path := filepath.Join(t.TempDir(), "state.db")
		st, err := teststore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		fixtures[i] = st
		version, err := st.CurrentSchemaVersion(ctx)
		if err != nil || version != store.ExpectedSchemaVersion() {
			t.Fatalf("store %d schema = %d, %v", i, version, err)
		}
		requirements, err := st.Requirements(ctx)
		if err != nil || !slices.Equal(requirements, store.KnownRequirements()) {
			t.Fatalf("store %d requirements = %v, %v", i, requirements, err)
		}
		var runs int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
			t.Fatalf("store %d starts with %d runs: %v", i, runs, err)
		}
		if err := st.DB().QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = 'controller_authority_id'`).Scan(&authorities[i]); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("fixture permissions: %v, %v", info, err)
		}
	}
	if authorities[0] == authorities[1] {
		t.Fatal("fixtures share a controller authority")
	}
	if err := fixtures[0].CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures[1].GetRun(ctx, "r1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second fixture sees first fixture's run: %v", err)
	}
}

func TestOpenRefusesExistingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.Open(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("open existing file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "preserve" {
		t.Fatalf("existing file = %q, %v", got, err)
	}
}
