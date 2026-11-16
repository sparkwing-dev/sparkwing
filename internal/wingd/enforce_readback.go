package wingd

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// perf: the active runner lookup reads the store, and runner claim polls query the
// queue state twice a second.
const enforcementReadInterval = 5 * time.Second

// safety: status reads the active runner's limits back from the kernel, so a refused or absent
// limit never shows as enforced.
func (d *Daemon) budgetEnforcement(machineCores float64, machineMemory uint64) *wingwire.BudgetEnforcement {
	if !d.cfg.Budget.Enforcing() {
		return nil
	}
	now := d.now()
	d.enforcementMu.Lock()
	defer d.enforcementMu.Unlock()
	if d.enforcement != nil && now.Sub(d.enforcementAt) < enforcementReadInterval {
		out := *d.enforcement
		return &out
	}
	var out *wingwire.BudgetEnforcement
	if runtime.GOOS != "linux" {
		out = &wingwire.BudgetEnforcement{Reason: unenforcedPlatformReason(runtime.GOOS)}
	} else if pid, reason := d.activeRunnerPID(); reason != "" {
		out = &wingwire.BudgetEnforcement{Reason: reason}
	} else {
		sensor := d.container
		if sensor == nil {
			sensor = newContainerSensor(d.cfg.ContainerRoot)
		}
		cores, memory := sensor.processLimits(pid)
		nice, _ := processNice(sensor.root, pid)
		out = runnerEnforcement(d.cfg.Budget, machineCores, machineMemory, cores, memory, nice)
	}
	d.enforcement, d.enforcementAt = out, now
	copied := *out
	return &copied
}

func unenforcedPlatformReason(goos string) string {
	if goos == "darwin" {
		return "macOS gives Sparkwing jobs background scheduling only"
	}
	return "this platform has no OS limit for the local runner"
}

func (d *Daemon) activeRunnerPID() (int, string) {
	registry, ok := d.cfg.Runs.(LocalRunnerStore)
	if !ok {
		return 0, "no local controller hosts a runner here"
	}
	subject, err := CurrentLocalSubject()
	if err != nil {
		return 0, "the local runner's owner is unknown: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	instance, err := registry.ActiveLocalRunner(ctx, subject)
	if err != nil {
		return 0, "no local runner is running yet"
	}
	alive, err := LocalRunnerProcessAlive(instance)
	if err != nil || !alive {
		return 0, "no local runner is running yet"
	}
	return instance.PID, ""
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
