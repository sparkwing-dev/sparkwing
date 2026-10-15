package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCloneAndFetchUseOriginDirectly(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "clone")
	command := exec.Command("git", "init", "--bare", source)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	t.Setenv("SPARKWING_GITCACHE_URL", "http://invalid.example")
	if err := Clone(context.Background(), source, dest); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "keep")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), source, dest); err == nil {
		t.Fatal("clone replaced an existing destination")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "retained" {
		t.Fatalf("destination changed: %s %v", raw, err)
	}
}
