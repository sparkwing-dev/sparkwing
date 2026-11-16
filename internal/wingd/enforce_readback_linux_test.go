//go:build linux

package wingd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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
	pid := os.Getpid()
	birth, err := admissionProcessBirth(pid)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	unit := filepath.Join(root, "sys", "fs", "cgroup", "app.slice", "sparkwing-runner-x.service")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	proc := filepath.Join(root, "proc", strconv.Itoa(pid))
	write(filepath.Join(proc, "cgroup"), "0::/app.slice/sparkwing-runner-x.service\n")
	write(filepath.Join(proc, "stat"), strconv.Itoa(pid)+" (runner) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 30 10 8 0 1 0\n")
	write(filepath.Join(unit, "cpu.max"), "200000 100000\n")
	budget, err := userconfig.ParseBudget("2,enforce")
	if err != nil {
		t.Fatal(err)
	}
	runner := &activeRunnerFixture{instance: localhostwire.LocalRunnerInstance{ID: "r", PID: pid, ProcessBirth: birth}}
	now := time.Unix(1_700_000_000, 0)
	d := &Daemon{cfg: Config{Budget: budget, ContainerRoot: root, Runs: runner, Now: func() time.Time { return now }}}

	if got := d.budgetEnforcement(16, 32<<30); !got.Applied || got.CPUCores != 2 {
		t.Fatalf("an applied quota read %+v", got)
	}
	write(filepath.Join(unit, "cpu.max"), "max 100000\n")
	if got := d.budgetEnforcement(16, 32<<30); got.Applied || got.Reason != "the local runner's cgroup has no CPU limit" {
		t.Fatalf("a removed quota still read as enforced: %+v", got)
	}
	if runner.lookups != 1 {
		t.Fatalf("active runner looked up %d times within the reuse window", runner.lookups)
	}
	now = now.Add(enforcementReadInterval)
	d.budgetEnforcement(16, 32<<30)
	if runner.lookups != 2 {
		t.Fatalf("active runner lookup was not refreshed after the reuse window: %d", runner.lookups)
	}
}
