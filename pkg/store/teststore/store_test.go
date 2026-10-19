package teststore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestOpenKeepsStateSeparate(t *testing.T) {
	ctx := context.Background()
	var fixtures [2]*store.Store
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
		var runs int
		if err := teststore.SQL(t, st).QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
			t.Fatalf("store %d starts with %d runs: %v", i, runs, err)
		}
		info, err := os.Stat(path)
		expectedMode := os.FileMode(0o600)
		if runtime.GOOS == "windows" {
			expectedMode = 0o666
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != expectedMode {
			t.Fatalf("fixture permissions: %v, %v", info, err)
		}
	}
	if _, err := teststore.SQL(t, fixtures[0]).ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,'r1',1,'demo','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, store.DefaultTeam, []byte(`{"version":1}`), []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	tenant := teststore.ForTeam(t, fixtures[1], store.DefaultTeam)
	if _, err := tenant.ReadRun(ctx, "r1"); !errors.Is(err, store.ErrNotFound) {
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
