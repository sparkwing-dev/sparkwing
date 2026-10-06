// Package testhome isolates tests from the machine owner's home directory.
package testhome

import (
	"runtime"
	"testing"
)

// Set redirects both shell and native home lookups so tests cannot read or write the owner's configuration.
func Set(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}
