package wingd

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// safety: status looks up the active runner and reads its limits back from the kernel on every query,
// so a refused, absent or removed limit, or an uncapped successor, never shows as enforced.
func (d *Daemon) budgetEnforcement(machineCores float64, machineMemory uint64) *wingwire.BudgetEnforcement {
	if !d.cfg.Budget.Enforcing() {
		return nil
	}
	if runtime.GOOS != "linux" {
		return &wingwire.BudgetEnforcement{Reason: unenforcedPlatformReason(runtime.GOOS)}
	}
	runner, reason := d.activeRunner()
	if reason != "" {
		return &wingwire.BudgetEnforcement{Reason: reason}
	}
	sensor := d.container
	if sensor == nil {
		sensor = newContainerSensor(d.cfg.ContainerRoot)
	}
	cores, memory := sensor.processLimits(runner.PID)
	nice, _ := processNice(sensor.root, runner.PID)
	return runnerEnforcement(d.cfg.Budget, machineCores, machineMemory, cores, memory, nice)
}

func unenforcedPlatformReason(goos string) string {
	if goos == "darwin" {
		return "macOS gives Sparkwing jobs background scheduling only"
	}
	return "this platform has no OS limit for the local runner"
}

func (d *Daemon) activeRunner() (localhostwire.LocalRunnerInstance, string) {
	registry, ok := d.cfg.Runs.(LocalRunnerStore)
	if !ok {
		return localhostwire.LocalRunnerInstance{}, "no local controller hosts a runner here"
	}
	subject, err := CurrentLocalSubject()
	if err != nil {
		return localhostwire.LocalRunnerInstance{}, "the local runner's owner is unknown: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	instance, err := registry.ActiveLocalRunner(ctx, subject)
	if err != nil {
		return localhostwire.LocalRunnerInstance{}, "no local runner is running yet"
	}
	if alive, err := LocalRunnerProcessAlive(instance); err != nil || !alive {
		return localhostwire.LocalRunnerInstance{}, "no local runner is running yet"
	}
	return instance, ""
}

// safety: memory counts only when the budget names it, as the runner unit sets MemoryMax only then.
func runnerEnforcement(budget userconfig.Budget, machineCores float64, machineMemory uint64, cores float64, memory uint64, nice int) *wingwire.BudgetEnforcement {
	out := &wingwire.BudgetEnforcement{CPUCores: cores, MemoryBytes: int64(memory), Nice: nice}
	var missing []string
	if budget.Cores > 0 || budget.CoresFraction > 0 {
		want := budget.CapCores(machineCores)
		switch {
		case cores <= 0:
			missing = append(missing, "the local runner's cgroup has no CPU limit")
		case cores > want+0.01:
			missing = append(missing, fmt.Sprintf("the local runner's CPU limit %.1f cores exceeds the %.1f-core budget", cores, want))
		}
	}
	if budget.MemoryBytes > 0 || budget.MemoryFraction > 0 {
		want := budget.CapMemory(machineMemory)
		switch {
		case memory == 0:
			missing = append(missing, "the local runner's cgroup has no memory limit")
		case memory > want:
			missing = append(missing, "the local runner's memory limit exceeds the budget")
		}
	}
	out.Applied = len(missing) == 0
	out.Reason = strings.Join(missing, "; ")
	return out
}

// hack: nice is field 19 of /proc/<pid>/stat; the command name in field 2 may hold spaces and
// parentheses, so fields are counted after its last ')'.
func processNice(root string, pid int) (int, bool) {
	stat := readWholeFile(filepath.Join(root, "proc", strconv.Itoa(pid), "stat"))
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 17 {
		return 0, false
	}
	nice, err := strconv.Atoi(fields[16])
	return nice, err == nil
}
