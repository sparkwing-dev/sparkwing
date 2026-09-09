// Package testleak runs a package's tests under a goroutine-leak check, so a
// test that starts a goroutine and never stops it fails the package.
package testleak

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

// safety: helper children inherit this, which is how they tell a re-exec of the
// suite's own binary from the suite.
const hostEnv = "SPARKWING_TESTLEAK_HOST"

var reExec = markHost()

func markHost() bool {
	host := os.Args[0]
	inherited := os.Getenv(hostEnv) == host
	if err := os.Setenv(hostEnv, host); err != nil {
		fmt.Fprintf(os.Stderr, "testleak: cannot mark this test binary (%v); processes "+
			"it re-execs will run the leak check on themselves\n", err)
	}
	return inherited
}

// Main runs m, verifies that no goroutine outlived the tests, and exits the
// process with the result. Call it from TestMain. Options given here apply to
// the calling package only; the house-wide ignores always apply.
//
// A helper process the tests spawn by re-executing the test binary runs its
// tests without the leak check. Such a child inherits an environment marker
// naming the binary the suite started from, so it recognizes itself. The check
// is meant to fail a package on a goroutine a test left behind; a helper that
// leaks one would otherwise fail the child with a goleak report that the parent
// can only relay as an unexplained helper crash.
func Main(m *testing.M, opts ...goleak.Option) {
	if reExec {
		os.Exit(m.Run()) //nolint:forbidigo // TestMain owns the exit code; Main is documented to exit.
	}
	goleak.VerifyTestMain(m, append(houseIgnores(), opts...)...)
}

// Check reports the goroutines still running, or nil when none outlived the
// tests. A TestMain with cleanup of its own to run between the tests and the
// exit needs this instead of Main, which exits the process itself.
//
// It reports nil in a helper process the tests spawned by re-executing the test
// binary, for the reason [Main] gives.
func Check(opts ...goleak.Option) error {
	if reExec {
		return nil
	}
	return goleak.Find(append(houseIgnores(), opts...)...)
}

func houseIgnores() []goleak.Option {
	return nil
}
