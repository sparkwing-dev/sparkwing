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

func TestPreparedIdentityFollowsLinkedDependencies(t *testing.T) {
	directory, dependency := t.TempDir(), t.TempDir()
	node := filepath.Join(t.TempDir(), "node")
	entry := filepath.Join(directory, "index.ts")
	linked := filepath.Join(directory, "linked")
	source := filepath.Join(dependency, "sdk.js")
	for _, path := range []string{node, entry, source} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dependency, linked); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	command := Command{node, entry}
	original, err := command.Identity(directory, "v22.23.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := command.Identity(directory, "v22.23.1")
	if err != nil || changed == original {
		t.Fatalf("linked source change not detected: %s, %v", changed, err)
	}
	if err := os.Symlink(directory, filepath.Join(dependency, "cycle")); err != nil {
		t.Fatal(err)
	}
	if _, err := command.Identity(directory, "v22.23.1"); err != nil {
		t.Fatalf("dependency cycle: %v", err)
	}
}
