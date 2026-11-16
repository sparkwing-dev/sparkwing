//go:build linux

package wingd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type activeRunnerFixture struct {
	instance localhostwire.LocalRunnerInstance
	lookups  int
}

func (*activeRunnerFixture) IsRunTerminal(wingwire.RunID) (bool, error) { return false, nil }

func (*activeRunnerFixture) FinalizeCancelledRuns([]string, string) error { return nil }

func (*activeRunnerFixture) Ready() error { return nil }

func (f *activeRunnerFixture) ActiveLocalRunner(context.Context, string) (localhostwire.LocalRunnerInstance, error) {
	f.lookups++
	return f.instance, nil
}

func (*activeRunnerFixture) RegisterLocalRunner(context.Context, string, localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error) {
	panic("unused")
}

func (*activeRunnerFixture) ReadLocalRunner(context.Context, string, string) (localhostwire.LocalRunnerInstance, error) {
	panic("unused")
}

func (*activeRunnerFixture) ActivateLocalRunner(context.Context, string, string) error {
	panic("unused")
}

func (*activeRunnerFixture) DrainLocalRunner(context.Context, string, string) error { panic("unused") }

func (*activeRunnerFixture) StopLocalRunner(context.Context, string, string) error { panic("unused") }

func TestBudgetEnforcementRereadsTheKernelOnEveryQuery(t *testing.T) {
	root, write := enforcementFixtureRoot(t)
	predecessor := liveRunner(t, os.Getpid(), "predecessor", root, write)
	unit := filepath.Join(root, "sys", "fs", "cgroup", "app.slice", "predecessor.service")
	write(filepath.Join(unit, "cpu.max"), "200000 100000\n")
	runner := &activeRunnerFixture{instance: predecessor}
	d := enforcingDaemon(t, root, runner)

	if got := d.budgetEnforcement(16, 32<<30); !got.Applied || got.CPUCores != 2 {
		t.Fatalf("an applied quota read %+v", got)
	}
	write(filepath.Join(unit, "cpu.max"), "max 100000\n")
	if got := d.budgetEnforcement(16, 32<<30); got.Applied || got.Reason != "the local runner's cgroup has no CPU limit" {
		t.Fatalf("a removed quota still read as enforced: %+v", got)
	}
}

func TestBudgetEnforcementFollowsAnUncappedSuccessorAtOnce(t *testing.T) {
	root, write := enforcementFixtureRoot(t)
	predecessor := liveRunner(t, os.Getpid(), "predecessor", root, write)
	write(filepath.Join(root, "sys", "fs", "cgroup", "app.slice", "predecessor.service", "cpu.max"), "200000 100000\n")
	successor := liveRunner(t, os.Getppid(), "successor", root, write)
	write(filepath.Join(root, "sys", "fs", "cgroup", "app.slice", "successor.service", "cpu.max"), "max 100000\n")
	runner := &activeRunnerFixture{instance: predecessor}
	d := enforcingDaemon(t, root, runner)

	if got := d.budgetEnforcement(16, 32<<30); !got.Applied {
		t.Fatalf("the capped predecessor read %+v", got)
	}
	runner.instance = successor
	if got := d.budgetEnforcement(16, 32<<30); got.Applied || got.Reason != "the local runner's cgroup has no CPU limit" {
		t.Fatalf("a live capped predecessor hid the uncapped active successor: %+v", got)
	}
	if runner.lookups != 2 {
		t.Fatalf("active runner looked up %d times for 2 queries", runner.lookups)
	}
}

func enforcementFixtureRoot(t *testing.T) (string, func(string, string)) {
	t.Helper()
	root := t.TempDir()
	return root, func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func liveRunner(t *testing.T, pid int, name, root string, write func(string, string)) localhostwire.LocalRunnerInstance {
	t.Helper()
	birth, err := admissionProcessBirth(pid)
	if err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(root, "proc", strconv.Itoa(pid))
	write(filepath.Join(proc, "cgroup"), "0::/app.slice/"+name+".service\n")
	write(filepath.Join(proc, "stat"), strconv.Itoa(pid)+" ("+name+") S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 30 10 8 0 1 0\n")
	return localhostwire.LocalRunnerInstance{ID: name, PID: pid, ProcessBirth: birth}
}

func enforcingDaemon(t *testing.T, root string, runner *activeRunnerFixture) *Daemon {
	t.Helper()
	budget, err := userconfig.ParseBudget("2,enforce")
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{cfg: Config{Budget: budget, ContainerRoot: root, Runs: runner}}
}
