//go:build !windows

package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureTriggerWorkRootPrivatizesOnlyDefaultRoot(t *testing.T) {
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(privateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureTriggerWorkRoot(privateRoot, true); err != nil {
		t.Fatal(err)
	}
	assertTriggerRootMode(t, privateRoot, 0o700)

	customRoot := filepath.Join(t.TempDir(), "custom")
	if err := ensureTriggerWorkRoot(customRoot, false); err != nil {
		t.Fatal(err)
	}
	assertTriggerRootMode(t, customRoot, 0o755)
}

func assertTriggerRootMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
