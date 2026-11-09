package pipelineentry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedIdentityDetectsRuntimeSourceAndInstalledSDKChanges(t *testing.T) {
	directory := t.TempDir()
	node := filepath.Join(t.TempDir(), "node")
	entry := filepath.Join(t.TempDir(), "index.ts")
	source := filepath.Join(directory, "helper.ts")
	sdk := filepath.Join(directory, "node_modules", "sdk.js")
	if err := os.MkdirAll(filepath.Dir(sdk), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{node, entry, source, sdk} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := Command{node, entry}
	original, err := command.Identity(directory, "v22.23.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{node, entry, source, sdk} {
		if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		changed, err := command.Identity(directory, "v22.23.1")
		if err != nil || changed == original {
			t.Fatalf("change to %s not detected: %s, %v", path, changed, err)
		}
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	changedVersion, err := command.Identity(directory, "v22.23.2")
	if err != nil || changedVersion == original {
		t.Fatalf("runtime version change not detected: %s, %v", changedVersion, err)
	}
	again, err := command.Identity(directory, "v22.23.1")
	if err != nil || again != original {
		t.Fatalf("identity did not recover: %s, %v", again, err)
	}
}
