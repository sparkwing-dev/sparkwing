package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestLocalReleaseRefusalPreservesPopulatedStoreAndShowsRemedy(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPARKWING_HOME", filepath.Join(home, ".sparkwing"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	db := filepath.Join(home, ".sparkwing", "state.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := teststore.Open(t, db)
	if err != nil {
		t.Fatal(err)
	}
	seedErr := teststore.SeedRun(state, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "retained", Pipeline: "hello", Status: "success"}})
	closeErr := state.Close()
	if seedErr != nil || closeErr != nil {
		t.Fatalf("populate operational store: %v %v", seedErr, closeErr)
	}
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	err = runSparkwing([]string{"-C", root, "run", "release", "--sw-dry-run", "--sw-no-update", "--sw-allow-unadmitted", "-o=plain"})
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.IsolatedHomeRequired {
		t.Fatalf("release preview did not refuse the operational home: %v", err)
	}
	var diagnostic bytes.Buffer
	writeFailure(&diagnostic, err)
	if !strings.Contains(diagnostic.String(), `HOME="$(mktemp -d)" SPARKWING_HOME="$(mktemp -d)"`) {
		t.Fatalf("release refusal hid its recovery command: %s", &diagnostic)
	}
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("release discovery changed the populated operational store")
	}
}
