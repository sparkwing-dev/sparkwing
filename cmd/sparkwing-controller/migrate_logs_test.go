package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLogsNeedsTheServicesRoot(t *testing.T) {
	err := runMigrateLogs(nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("migrate-logs without --root = %v, want a refusal naming it", err)
	}
}

// A dry run over a logs volume counts its logs and writes nothing beside
// the controller's state database.
func TestMigrateLogsDryRunCountsAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	root := t.TempDir()
	run := filepath.Join(root, "runs", "run-1")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "build.log"), []byte("line 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runMigrateLogs([]string{"--root", root, "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would convert 1 run(s), 1 attempt(s), 7 byte(s)") {
		t.Fatalf("dry run said %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "lo")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote under the state directory: %v", err)
	}
}
