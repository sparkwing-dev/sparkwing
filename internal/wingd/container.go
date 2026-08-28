package wingd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type containerSensor struct {
	root string
	now  func() time.Time

	mu        sync.Mutex
	haveUsage bool
	lastUsage uint64
	lastAt    time.Time
}

func newContainerSensor(root string) *containerSensor {
	if root == "" {
		root = "/"
	}
	return &containerSensor{root: root, now: time.Now}
}

func containerSensorFor(cfg Config) *containerSensor {
	switch {
	case cfg.ContainerRoot != "":
		return newContainerSensor(cfg.ContainerRoot)
	case cfg.Sampler == nil:
		return newContainerSensor("/")
	default:
		return nil
	}
}

func (s *containerSensor) capacityLimits() (cores float64, memBytes uint64) {
	if s == nil {
		return 0, 0
	}
	if dir, ok := s.v2Dir(); ok {
		if c, ok := parseCPUMax(s.readTrim(filepath.Join(dir, "cpu.max"))); ok {
			cores = c
		}
		if n, ok := parseCpuset(s.readTrim(filepath.Join(dir, "cpuset.cpus.effective"))); ok {
			if cores == 0 || float64(n) < cores {
				cores = float64(n)
			}
		}
		if m, ok := parseMemMax(s.readTrim(filepath.Join(dir, "memory.max"))); ok {
			memBytes = m
		}
	}
	if cores == 0 && memBytes == 0 {
		cores, memBytes = s.capacityV1()
	}
	return cores, memBytes
}

func (s *containerSensor) apply(stat HostStat) HostStat {
	if s == nil {
		return stat
	}
	cores, memBytes := s.capacityLimits()
	coresClamped := cores > 0 && cores < stat.TotalCores
	memClamped := memBytes > 0 && memBytes < stat.TotalMemoryBytes
	if coresClamped {
		stat.TotalCores = cores
	}
	if memClamped {
		stat.TotalMemoryBytes = memBytes
	}
	if coresClamped {
		if load, ok := s.cpuUsageCores(); ok {
			stat.LoadAverage = load
			stat.LoadMeasured = true
		}
	}
	if memClamped {
		if used, ok := s.usedMemory(); ok {
			free := uint64(0)
			if memBytes > used {
				free = memBytes - used
			}
			stat.FreeMemoryBytes = free
			stat.MemoryMeasured = true
		}
	}
	if stat.FreeMemoryBytes > stat.TotalMemoryBytes {
		stat.FreeMemoryBytes = stat.TotalMemoryBytes
	}
	return stat
}

func (s *containerSensor) v2Dir() (string, bool) {
	rel, ok := cgroupV2Path(s.readFile(filepath.Join("proc", "self", "cgroup")))
	if !ok {
		return "", false
	}
	base := filepath.Join(s.root, "sys", "fs", "cgroup")
	for _, dir := range []string{filepath.Join(base, rel), base} {
		if s.hasFile(filepath.Join(dir, "cpu.max")) || s.hasFile(filepath.Join(dir, "memory.max")) {
			return dir, true
		}
	}
	return "", false
}

func (s *containerSensor) capacityV1() (cores float64, memBytes uint64) {
	content := s.readFile(filepath.Join("proc", "self", "cgroup"))
	base := filepath.Join(s.root, "sys", "fs", "cgroup")
	if p, ok := cgroupV1Path(content, "cpu"); ok {
		for _, ctl := range []string{"cpu", "cpu,cpuacct"} {
			dir := filepath.Join(base, ctl, p)
			quota, qok := parseInt(s.readTrim(filepath.Join(dir, "cpu.cfs_quota_us")))
			period, pok := parseInt(s.readTrim(filepath.Join(dir, "cpu.cfs_period_us")))
			if qok && pok && quota > 0 && period > 0 {
				cores = float64(quota) / float64(period)
				break
			}
		}
	}
	if p, ok := cgroupV1Path(content, "memory"); ok {
		dir := filepath.Join(base, "memory", p)
		if m, ok := parseMemMax(s.readTrim(filepath.Join(dir, "memory.limit_in_bytes"))); ok {
			memBytes = m
		}
	}
	return cores, memBytes
}

func (s *containerSensor) cpuUsageCores() (float64, bool) {
	dir, ok := s.v2Dir()
	if !ok {
		return 0, false
	}
	usage, ok := parseUsageUsec(s.readWhole(filepath.Join(dir, "cpu.stat")))
	if !ok {
		return 0, false
	}
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, prevAt, had := s.lastUsage, s.lastAt, s.haveUsage
	s.lastUsage, s.lastAt, s.haveUsage = usage, now, true
	if !had {
		return 0, false
	}
	dt := now.Sub(prevAt).Seconds()
	if dt <= 0 || usage < prev {
		return 0, false
	}
	return float64(usage-prev) / 1e6 / dt, true
}

func (s *containerSensor) usedMemory() (uint64, bool) {
	dir, ok := s.v2Dir()
	if !ok {
		return 0, false
	}
	return parseUint(s.readTrim(filepath.Join(dir, "memory.current")))
}

func (s *containerSensor) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *containerSensor) readFile(rel string) string {
	return readWholeFile(filepath.Join(s.root, rel))
}

func (s *containerSensor) readWhole(path string) string {
	return readWholeFile(path)
}

func readWholeFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *containerSensor) readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *containerSensor) hasFile(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cgroupV2Path(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			return parts[2], true
		}
	}
	return "", false
}

func cgroupV1Path(content, controller string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, c := range strings.Split(parts[1], ",") {
			if c == controller {
				return parts[2], true
			}
		}
	}
	return "", false
}

func parseCPUMax(content string) (float64, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 || fields[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || quota <= 0 {
		return 0, false
	}
	period := float64(cgroupCPUPeriodUS)
	if len(fields) >= 2 {
		p, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || p <= 0 {
			return 0, false
		}
		period = p
	}
	return quota / period, true
}

const cgroupUnlimitedMem = uint64(1) << 62

func parseMemMax(content string) (uint64, bool) {
	content = strings.TrimSpace(content)
	if content == "" || content == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(content, 10, 64)
	if err != nil || v == 0 || v >= cgroupUnlimitedMem {
		return 0, false
	}
	return v, true
}

func parseCpuset(content string) (int, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return 0, false
	}
	total := 0
	for _, part := range strings.Split(content, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			if _, err := strconv.Atoi(part); err != nil {
				return 0, false
			}
			total++
			continue
		}
		start, err1 := strconv.Atoi(strings.TrimSpace(lo))
		end, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || end < start {
			return 0, false
		}
		total += end - start + 1
	}
	if total == 0 {
		return 0, false
	}
	return total, true
}

func parseUsageUsec(content string) (uint64, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return parseUint(fields[1])
		}
	}
	return 0, false
}

func parseUint(s string) (uint64, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseInt(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
