//go:build !windows

package testhome

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempDirSupportsUnixSocketsWithLongTMPDIR(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("long", 30))
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", root)
	var dir string
	t.Run("socket", func(t *testing.T) {
		dir = TempDir(t)
		listener, err := net.Listen("unix", filepath.Join(dir, "docker.sock"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := listener.Close(); err != nil {
				t.Error(err)
			}
		})
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temporary socket directory remains after cleanup: %v", err)
	}
}
