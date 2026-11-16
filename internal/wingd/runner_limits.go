package wingd

import (
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// RunnerLimits are the OS limits a supervised local runner, and every job it hosts,
// starts under. The zero value applies none.
type RunnerLimits struct {
	CPUCores    float64
	MemoryBytes uint64
	Nice        int
}

// LocalRunnerLimits derives runner limits from a machine budget that asks for
// enforcement, against the same capacity admission measures.
func LocalRunnerLimits(budget userconfig.Budget) (RunnerLimits, error) {
	if !budget.Enforcing() {
		return RunnerLimits{}, nil
	}
	stat, err := sampleHost()
	if err != nil {
		return RunnerLimits{}, fmt.Errorf("wingd: measure machine for runner limits: %w", err)
	}
	if cores, memory := newContainerSensor("/").capacityLimits(); cores > 0 || memory > 0 {
		if cores > 0 && cores < stat.TotalCores {
			stat.TotalCores = cores
		}
		if memory > 0 && memory < stat.TotalMemoryBytes {
			stat.TotalMemoryBytes = memory
		}
	}
	limits := RunnerLimits{Nice: backgroundNice}
	if budget.Cores > 0 || budget.CoresFraction > 0 {
		limits.CPUCores = budget.CapCores(stat.TotalCores)
	}
	if budget.MemoryBytes > 0 || budget.MemoryFraction > 0 {
		limits.MemoryBytes = budget.CapMemory(stat.TotalMemoryBytes)
	}
	return limits, nil
}

func (l RunnerLimits) systemdArguments() []string {
	var args []string
	if l.CPUCores > 0 {
		percent := max(1, int64(math.Floor(l.CPUCores*100)))
		args = append(args, "--property=CPUQuota="+strconv.FormatInt(percent, 10)+"%")
	}
	if l.MemoryBytes > 0 {
		args = append(args, "--property=MemoryMax="+strconv.FormatUint(l.MemoryBytes, 10))
	}
	if l.Nice != 0 {
		args = append(args, "--nice="+strconv.Itoa(l.Nice))
	}
	return args
}

// Matches reports whether two limit sets would bind a runner identically.
func (l RunnerLimits) Matches(other RunnerLimits) bool {
	return math.Abs(l.CPUCores-other.CPUCores) < 0.01 && l.MemoryBytes == other.MemoryBytes && l.Nice == other.Nice
}

// LocalRunnerAppliedLimits reads the limits the service manager set on the runner's own
// cgroup, not its ancestors', so they compare directly with the limits it was started with.
func LocalRunnerAppliedLimits(pid int) (RunnerLimits, bool) {
	if runtime.GOOS != "linux" || pid <= 0 {
		return RunnerLimits{}, false
	}
	sensor := newContainerSensor("/")
	dirs := sensor.v2Ancestry(strconv.Itoa(pid))
	nice, ok := processNice("/", pid)
	if len(dirs) == 0 || !ok {
		return RunnerLimits{}, false
	}
	limits := RunnerLimits{Nice: nice}
	if cores, ok := parseCPUMax(sensor.readTrim(filepath.Join(dirs[0], "cpu.max"))); ok {
		limits.CPUCores = cores
	}
	if memory, ok := parseMemMax(sensor.readTrim(filepath.Join(dirs[0], "memory.max"))); ok {
		limits.MemoryBytes = memory
	}
	return limits, true
}
