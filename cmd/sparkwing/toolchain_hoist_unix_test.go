//go:build !windows

package main

import (
	"errors"
	"testing"
)

// The switch has to settle before the command starts a daemon advertising the
// installed version, mints a run, binds an index, or builds a ref worktree.
func TestDispatchRunSwitchesBeforeStartingADaemon(t *testing.T) {
	seedToolchainStore(t, "v0.99.0", releaseFixture("v0.99.0"))
	cutTheNetwork(t)
	withInstalledVersion(t, "v0.38.2")
	withToolchainActive(t, "")
	t.Setenv(toolchainModeEnv, "")

	repo := t.TempDir()
	writeSparkwingModuleIn(t, repo, pinModule("v0.99.0"))

	var order []string
	sentinel := errors.New("exec stub")
	prevExec, prevDaemon := toolchainExecFn, ensureRunDaemonFn
	toolchainExecFn = func(string, []string, []string) error {
		order = append(order, "switch")
		return sentinel
	}
	ensureRunDaemonFn = func() { order = append(order, "daemon") }
	t.Cleanup(func() { toolchainExecFn, ensureRunDaemonFn = prevExec, prevDaemon })

	err := dispatchRun([]string{"hello", "-C", repo})
	if !errors.Is(err, sentinel) {
		t.Fatalf("dispatchRun = %v, want the exec stub's error", err)
	}
	if len(order) != 1 || order[0] != "switch" {
		t.Fatalf("call order = %v, want the switch alone", order)
	}
}
