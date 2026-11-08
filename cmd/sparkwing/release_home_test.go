package main

import (
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	command := exec.CommandContext(t.Context(), os.Args[0], "run", "release", "--sw-dry-run", "--sw-no-update", "--sw-allow-unadmitted", "-o=plain")
	command.Dir = root
	diagnostic, err := command.CombinedOutput()
	var refusal *exec.ExitError
	if !errors.As(err, &refusal) || refusal.ExitCode() != 1 {
		t.Fatalf("release preview did not refuse the operational home: %v", err)
	}
	if !strings.Contains(string(diagnostic), "pipeline requires an isolated Sparkwing home") ||
		!strings.Contains(string(diagnostic), `HOME="$(mktemp -d)" SPARKWING_HOME="$(mktemp -d)"`) {
		t.Fatal("release refusal hid its recovery command")
	}
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("release discovery changed the populated operational store")
	}
}
