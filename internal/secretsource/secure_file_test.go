package secretsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestFilesystemSecretRefusesEscapingSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "secrets.env")
	if err := fssecure.WriteFile(target, []byte("TOKEN=outside\n")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "secrets.env")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	resolver, err := fromSpec(backends.Spec{Type: backends.TypeFilesystem, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	value, _, err := resolver.Resolve(t.Context(), "TOKEN")
	if err == nil || value != "" {
		t.Fatalf("escaping filesystem secret = %q, %v", value, err)
	}
}
