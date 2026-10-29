// Package testleak runs a package's tests under a goroutine-leak check, so a
// test that starts a goroutine and never stops it fails the package, and inside
// a private home, so a test that forgets to give itself one never reaches the
// developer's own files.
package testleak

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.uber.org/goleak"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// safety: helper children inherit this, which is how they tell a re-exec of the
// suite's own binary from the suite.
const hostEnv = "SPARKWING_TESTLEAK_HOST"

var reExec = markHost()

// safety: entered while the package initializes rather than in Main, so a
// TestMain that calls only Check is sandboxed too, and before any test runs.
var sandbox = enterSandbox()

func markHost() bool {
	host := os.Args[0]
	inherited := os.Getenv(hostEnv) == host
	if err := os.Setenv(hostEnv, host); err != nil {
		fmt.Fprintf(os.Stderr, "testleak: cannot mark this test binary (%v); processes "+
			"it re-execs will run the leak check on themselves\n", err)
	}
	return inherited
}

// enterSandbox points HOME, SPARKWING_HOME and XDG_CONFIG_HOME into a fresh
// directory, laid out the way a real home is, and returns its root. A helper
// the suite re-execs inherits the parent's sandbox and must not replace it.
func enterSandbox() string {
	if reExec {
		return ""
	}
	// safety: the Go caches and settings default to paths under HOME, so moving
	// HOME without pinning them sends every build a test runs to an empty cache
	// and a module download.
	goEnv := resolveGoEnv()
	// safety: named rather than created, because a helper started with a
	// replaced environment cannot tell it is one and never removes what it
	// made; the code under test creates the directories it writes.
	root := filepath.Join(os.TempDir(), fmt.Sprintf("sparkwing-test-%d-%x", os.Getpid(), rand.Uint64()))
	home := filepath.Join(root, "home")
	env := map[string]string{
		"HOME":            home,
		"SPARKWING_HOME":  filepath.Join(home, ".sparkwing"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		// Go telemetry starts detached uploaders that can recreate a removed home.
		"TEST_TELEMETRY_DIR": os.DevNull,
	}
	if runtime.GOOS == "windows" {
		env["USERPROFILE"] = home
	}
	for k, v := range goEnv {
		if v != "" {
			env[k] = v
		}
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "testleak: cannot enter the sandbox home %s: %v\n", root, err)
			os.Exit(2) //nolint:forbidigo // tests must not run against the developer's real home.
		}
	}
	return root
}

func resolveGoEnv() map[string]string {
	out, err := procenv.HostCommand("go", "env", "-json", "GOENV", "GOPATH", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	var env map[string]string
	if json.Unmarshal(out, &env) != nil {
		return nil
	}
	return env
}

func leaveSandbox() {
	if sandbox == "" {
		return
	}
	if err := os.RemoveAll(sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "testleak: remove sandbox home %s: %v\n", sandbox, err)
	}
	sandbox = ""
}

// Main runs m, verifies that no goroutine outlived the tests, removes the
// sandbox home, and exits the process with the result. Call it from TestMain.
// Options given here apply to the calling package only; the house-wide ignores
// always apply.
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
	cleanup := goleak.Cleanup(func(code int) {
		leaveSandbox()
		os.Exit(code) //nolint:forbidigo // TestMain owns the exit code; Main is documented to exit.
	})
	goleak.VerifyTestMain(m, append(append(houseIgnores(), opts...), cleanup)...)
}

// Check reports the goroutines still running, or nil when none outlived the
// tests, and removes the sandbox home. A TestMain with cleanup of its own to
// run between the tests and the exit needs this instead of Main, which exits
// the process itself; Check is the last thing such a TestMain does.
//
// It reports nil in a helper process the tests spawned by re-executing the test
// binary, for the reason [Main] gives.
func Check(opts ...goleak.Option) error {
	if reExec {
		return nil
	}
	defer leaveSandbox()
	return goleak.Find(append(houseIgnores(), opts...)...)
}

func houseIgnores() []goleak.Option {
	return nil
}
