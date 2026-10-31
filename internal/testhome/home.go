// Package testhome isolates tests from the machine owner's home directory.
package testhome

import (
	"os"
	"runtime"
	"testing"
)

// TempDir creates a private directory without the test name in its path, so
// Unix sockets fit within sun_path. It is removed when the test ends.
func TempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

// Set redirects HOME, and USERPROFILE on Windows, to home. Callers must clear
// XDG_CONFIG_HOME and SPARKWING_CONFIG themselves when they could point elsewhere.
func Set(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}
