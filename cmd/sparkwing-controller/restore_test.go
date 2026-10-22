package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
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
	if err := invoke("reconcile"); err == nil {
		t.Fatal("removed reconcile command was accepted")
	}
	if err := invoke("acknowledge"); err != nil {
		t.Fatal(err)
	}
	var state store.RestoreState
	if err := json.Unmarshal(out.Bytes(), &state); err != nil || state.Pending || state.AcknowledgedAt == 0 {
		t.Fatalf("finished restore = %+v, %v", state, err)
	}
}

func TestRestoreUsesSeparatePersistentEpochDirectory(t *testing.T) {
	home, creds, epochDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := credentials.LoadOrCreateEpoch(epochDir)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := runRestore([]string{"prepare", "--credentials-dir", creds, "--credential-epoch-dir", epochDir}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	after, err := credentials.LoadOrCreateEpoch(epochDir)
	if err != nil || after == before {
		t.Fatalf("separate epoch did not rotate: %v", err)
	}
	st, err = store.Open(filepath.Join(home, "state.db"), store.WithCredentialEpoch(after))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDeploymentRecord(t.Context(), filepath.Join(home, "state.db"), deployment.Config{}, creds, epochDir, st); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	adminStore, err := openDeclaredStore(t.Context(), filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := adminStore.Close(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, creds} {
		if _, err := os.Stat(filepath.Join(dir, "credential-epoch")); !os.IsNotExist(err) {
			t.Fatalf("epoch appeared outside its persistent directory %s: %v", dir, err)
		}
	}
}
