package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultForHomeIgnoresAmbientSelectors(t *testing.T) {
	root := t.TempDir()
	account := filepath.Join(root, "account")
	private := filepath.Join(root, "private")
	t.Setenv("HOME", private)
	t.Setenv("SPARKWING_HOME", filepath.Join(private, "state"))
	if !PathsAt(filepath.Join(account, ".sparkwing")).IsDefaultForHome(account) {
		t.Fatal("account default was redirected by ambient environment")
	}
	if PathsAt(filepath.Join(private, ".sparkwing")).IsDefaultForHome(account) {
		t.Fatal("private default counted as account default")
	}
	if PathsAt(".sparkwing").IsDefaultForHome("") {
		t.Fatal("empty account home matched a default")
	}
}

func TestDefaultForHomeRecognizesSymlink(t *testing.T) {
	account := t.TempDir()
	state := filepath.Join(account, ".sparkwing")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(account, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if !PathsAt(filepath.Join(alias, ".sparkwing")).IsDefaultForHome(account) {
		t.Fatal("symlinked account default counted as private")
	}
	if !PathsAt(state).IsDefaultForHome(alias) {
		t.Fatal("symlinked account home counted as private")
	}
}
