// Package testhome isolates tests from the machine owner's home directory.
package testhome

import (
	"runtime"
	"testing"
)

// Set redirects HOME, and USERPROFILE on Windows, to home. Callers must clear
// XDG_CONFIG_HOME and SPARKWING_CONFIG themselves when they could point elsewhere.
func Set(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}
