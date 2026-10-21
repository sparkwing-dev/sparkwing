package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRestoreCommandRequiresExistingStoreAndAlwaysRotates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	var out, stderr bytes.Buffer
	invoke := func(verb string) error {
		out.Reset()
		return runRestore([]string{verb}, &out, &stderr)
	}
	if err := invoke("prepare"); err == nil {
		t.Fatal("restore created an empty database")
	}
	path := filepath.Join(home, "state.db")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed restore created state: %v", err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := credentials.LoadOrCreateEpoch(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := invoke("prepare"); err != nil {
		t.Fatal(err)
	}
	rotated, err := credentials.LoadOrCreateEpoch(home)
	if err != nil || rotated == before {
		t.Fatalf("epoch did not rotate: %v", err)
	}
	if err := invoke("prepare"); err != nil {
		t.Fatal(err)
	}
	again, err := credentials.LoadOrCreateEpoch(home)
	if err != nil || again == rotated {
		t.Fatalf("preparation trusted a pending state restored from a backup: %v", err)
	}
	if err := invoke("acknowledge"); err == nil {
		t.Fatal("restore skipped required reconciliation")
	}
	if err := invoke("reconcile"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("acknowledge"); err != nil {
		t.Fatal(err)
	}
	var state store.RestoreState
	if err := json.Unmarshal(out.Bytes(), &state); err != nil || state.Pending || !state.Reconciled || state.AcknowledgedAt == 0 {
		t.Fatalf("finished restore = %+v, %v", state, err)
	}
}
