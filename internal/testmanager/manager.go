// Package testmanager isolates native user-manager operations in tests.
package testmanager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testleak"
)

// OptInEnv authorizes tests that exercise a real user service manager.
const (
	OptInEnv = "SPARKWING_TEST_USER_MANAGER"
	guardEnv = "SPARKWING_TEST_MANAGER_GUARD_DIR"
)

// Enabled reports whether this test invocation selected real-manager coverage.
func Enabled() bool { return os.Getenv(OptInEnv) == "1" }

// Require skips real-manager coverage unless the dedicated qualification lane opted in.
func Require(t testing.TB) {
	t.Helper()
	if !Enabled() {
		t.Skip("real user-manager integration: set " + OptInEnv + "=1 in a quiet service-qualification window")
	}
}

// Run fails a package if an ordinary test attempts a native manager command,
// including attempts whose errors the test otherwise handles successfully.
func Run(run func() int) (code int) {
	if Enabled() || runtime.GOOS == "windows" {
		return run()
	}
	root := os.Getenv(guardEnv)
	// #nosec G703 -- the guard directory comes from this process's own environment, set by the test run that created it
	_, err := os.Stat(filepath.Join(root, "owner"))
	if root == "" || err != nil {
		dir, err := os.MkdirTemp("", "sparkwing-manager-guard-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() {
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintln(os.Stderr, "remove user-manager guard:", err)
				code = 1
			}
		}()
		if err := writeGuard(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		root = dir
		// safety: ambient service ownership is not a fixture input; scope tests set it explicitly.
		restoreInvocation, err := setenv("INVOCATION_ID", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer restoreEnvironment(&code, restoreInvocation)
	}
	restoreRoot, err := setenv(guardEnv, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer restoreEnvironment(&code, restoreRoot)
	if !containsPath(os.Getenv("PATH"), root) {
		restorePath, err := setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer restoreEnvironment(&code, restorePath)
	}
	code = run()
	// #nosec G703 -- the guard directory is this run's own or one inherited from the test run that created it
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "verify user-manager guard:", err)
		return 1
	}
	if len(calls) > 0 {
		fmt.Fprintf(os.Stderr, "unexpected user-manager command %s; inject a fake or opt into %s\n", strings.TrimSpace(string(calls)), OptInEnv)
		return 1
	}
	return code
}

func writeGuard(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "owner"), []byte("test manager guard\n"), 0o600); err != nil {
		return err
	}
	for _, name := range []string{"systemctl", "systemd-run", "launchctl"} {
		body := "#!/bin/sh\nset -eu\numask 077\nprintf '%s\\n' " + shellWord(name) + " > " + shellWord(filepath.Join(dir, "calls")) + "\nexit 97\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Scope fakes the systemd-run scope adapter while executing its payload normally.
func Scope(t testing.TB) {
	t.Helper()
	fake(t, "systemd-run", "[ \"$1\" = --user ] && [ \"$2\" = --scope ] && [ \"$3\" = --quiet ] && [ \"$4\" = -- ] || exit 90\nshift 4\nexec \"$@\"\n")
}

// Fail makes a selected manager fail without contacting the user's manager.
func Fail(t testing.TB, name string) { t.Helper(); fake(t, name, "exit 73\n") }

func fake(t testing.TB, name, body string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	script := "#!/bin/sh\nset -eu\nprintf called > " + shellWord(marker) + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("fake %s was not exercised: %v", name, err)
		}
	})
}

func containsPath(path, directory string) bool {
	for _, entry := range filepath.SplitList(path) {
		if entry == directory {
			return true
		}
	}
	return false
}
func shellWord(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func setenv(name, value string) (func() error, error) {
	before, exists := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil {
		return nil, err
	}
	return func() error {
		if exists {
			return os.Setenv(name, before)
		}
		return os.Unsetenv(name)
	}, nil
}

func restoreEnvironment(code *int, restore func() error) {
	if err := restore(); err != nil {
		fmt.Fprintln(os.Stderr, "restore test environment:", err)
		*code = 1
	}
}

// Main runs ordinary package tests behind the manager guard and the existing leak check.
func Main(m *testing.M) {
	code := Run(func() int {
		code := m.Run()
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return code
	})
	os.Exit(code) //nolint:forbidigo // TestMain owns the exit status.
}
