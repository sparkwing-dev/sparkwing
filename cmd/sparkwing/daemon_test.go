package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonRecoverStateRequiresConsentAndPreservesUnreadableBytes(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, "wingd")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "state.json")
	want := []byte("{truncated")
	if err := os.WriteFile(statePath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SPARKWING_HOME", home)
	err := runDaemon([]string{"recover-state"})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("recovery without consent error = %v", err)
	}
	if got, err := os.ReadFile(statePath); err != nil || string(got) != string(want) {
		t.Fatalf("recovery without consent changed state: %q, %v", got, err)
	}

	var runErr error
	captureStdout(t, func() {
		t.Setenv("SPARKWING_HOME", home)
		runErr = runDaemon([]string{"recover-state", "--yes"})
	})
	if runErr != nil {
		t.Fatalf("recover unreadable state: %v", runErr)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("unreadable state still blocks startup: %v", err)
	}
	matches, err := filepath.Glob(statePath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("preserved recovery files = %v, err %v", matches, err)
	}
	if got, err := os.ReadFile(matches[0]); err != nil || string(got) != string(want) {
		t.Fatalf("preserved recovery bytes = %q, %v", got, err)
	}
}
