//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDispatchRunSwitchesBeforeLocalIntake(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	calls := []string{}
	sentinel := errors.New("selected engine failed")
	err := dispatchRunWithEngine([]string{"hello"}, func(got string) error {
		calls = append(calls, "switch")
		if got != dir {
			t.Fatalf("selected source = %s", got)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || len(calls) != 1 || calls[0] != "switch" {
		t.Fatalf("dispatch = %v calls=%q", err, calls)
	}
}
