package fssecure_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestMkdirPrivateTempCreatesOnePrivateDirectoryUnderParent(t *testing.T) {
	parent := t.TempDir()
	directory, err := fssecure.MkdirPrivateTemp(parent, "probe-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(directory) }()
	if filepath.Dir(directory) != parent || !strings.HasPrefix(filepath.Base(directory), "probe-") {
		t.Fatalf("private temporary directory = %q", directory)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("private temporary path mode = %s", info.Mode())
	}
	if _, err := fssecure.MkdirPrivateTemp(parent, "nested/probe-"); err == nil {
		t.Fatal("MkdirPrivateTemp accepted a multi-component prefix")
	}
}
