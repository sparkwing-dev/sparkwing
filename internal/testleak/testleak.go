// Package testleak runs a package's tests under a goroutine-leak check, so a
// test that starts a goroutine and never stops it fails the package.
package testleak

import (
	"testing"

	"go.uber.org/goleak"
)

// Main runs m, verifies that no goroutine outlived the tests, and exits the
// process with the result. Call it from TestMain. Options given here apply to
// the calling package only; the house-wide ignores always apply.
func Main(m *testing.M, opts ...goleak.Option) {
	goleak.VerifyTestMain(m, append(houseIgnores(), opts...)...)
}

// Check reports the goroutines still running, or nil when none outlived the
// tests. A TestMain with cleanup of its own to run between the tests and the
// exit needs this instead of Main, which exits the process itself.
func Check(opts ...goleak.Option) error {
	return goleak.Find(append(houseIgnores(), opts...)...)
}

func houseIgnores() []goleak.Option {
	return nil
}
