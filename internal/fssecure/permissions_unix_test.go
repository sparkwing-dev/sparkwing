//go:build !windows

package fssecure

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureDirDoesNotBroadenExistingOwnerAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, root, 0o500)
}

func TestPrivateCreationIgnoresPermissiveUmask(t *testing.T) {
	if os.Getenv("SPARKWING_FSSECURE_UMASK_HELPER") == "1" {
		syscall.Umask(0)
		root := os.Getenv("SPARKWING_FSSECURE_TEST_ROOT")
		if err := os.MkdirAll(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := EnsureDir(root); err != nil {
			t.Fatal(err)
		}
		opened := filepath.Join(root, "opened")
		f, err := OpenFile(opened, os.O_CREATE|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		written := filepath.Join(root, "written")
		if err := os.WriteFile(written, []byte("legacy"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(written, []byte("private")); err != nil {
			t.Fatal(err)
		}
		assertPerm(t, root, dirMode)
		assertPerm(t, opened, fileMode)
		assertPerm(t, written, fileMode)
		return
	}

	root := filepath.Join(t.TempDir(), "private")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateCreationIgnoresPermissiveUmask$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_FSSECURE_UMASK_HELPER=1",
		"SPARKWING_FSSECURE_TEST_ROOT="+root,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("umask helper: %v\n%s", err, out)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
