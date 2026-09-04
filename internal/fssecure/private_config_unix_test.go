//go:build !windows

package fssecure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPrivateConfigRejectsGroupReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("token: private\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivateConfig(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("broad-mode error = %v", err)
	}
}

func TestPrivateConfigOpenerDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	link := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(target, []byte("token: private\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if file, err := openPrivateConfigFile(link); err == nil {
		_ = file.Close()
		t.Fatal("no-follow opener accepted a symlink")
	}
}
