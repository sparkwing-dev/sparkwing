package fssecure_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestSecurePrivateDirRejectsLinksAndFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("secured path mode = %s", info.Mode())
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateDir(file); err == nil {
		t.Fatal("SecurePrivateDir accepted a regular file")
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		if os.IsPermission(err) {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateDir(link); err == nil {
		t.Fatal("SecurePrivateDir accepted a directory link")
	}
}
