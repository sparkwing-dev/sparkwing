//go:build !windows

package localws

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDevEnvCreatesPrivateFile(t *testing.T) {
	root := t.TempDir()
	if err := writeDevEnv(root, "http://127.0.0.1:1234"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dev.env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("dev.env mode = %04o, want 0600", got)
	}
}
