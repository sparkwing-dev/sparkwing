package paths

import (
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
