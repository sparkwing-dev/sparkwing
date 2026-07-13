package wingd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// containerSensor reads the daemon process's own cgroup limits and live
// usage, so admission plans against the container it runs in rather than
// the host it sits on. All reads are rooted at root, "/" in production and
// a fixture tree in tests; a platform with no cgroup filesystem (macOS)
// finds nothing and leaves the host reading untouched. A nil sensor
// reports no limits, which is the behavior when detection is disabled.
type containerSensor struct {
	root string
	now  func() time.Time

	mu        sync.Mutex
	haveUsage bool
	lastUsage uint64
	lastAt    time.Time
}

// newContainerSensor builds a sensor rooted at root; an empty root reads
// the real filesystem at "/".
func newContainerSensor(root string) *containerSensor {
	if root == "" {
		root = "/"
	}
	return &containerSensor{root: root, now: time.Now}
}

// containerSensorFor selects the sensor a daemon runs with. An explicit
// ContainerRoot always wins, so a test can point detection at a fixture
// tree. Otherwise detection is enabled only for the real platform sampler
// (Sampler nil): a test that injects a fake host reading gets no cgroup
// clamp on top of it unless it asks for one.
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

// capacityLimits reports the cgroup's fixed capacity ceiling as a core
// count and a memory byte count. A zero for either dimension means the
// cgroup does not constrain it (unlimited, or no cgroup at all). It reads
// only the static limit files, never the usage baseline, so the daemon can
// size its ledger at startup without disturbing external-load sensing.
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

// apply clamps a host reading to the cgroup this process runs in. Capacity
// is lowered to the cgroup ceiling on each dimension the cgroup actually
// constrains below the host; on those same dimensions the live pressure is
// re-read from the cgroup (cpu.stat, memory.current) so external-load
// sensing measures the container rather than the machine. Dimensions the
// cgroup leaves unbounded pass through untouched, so a systemd host slice
// with no limits reads exactly as the host does.
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
		}
	}
	if memClamped {
		if used, ok := s.usedMemory(); ok {
			free := uint64(0)
			if memBytes > used {
				free = memBytes - used
			}
			stat.FreeMemoryBytes = free
		}
	}
	if stat.FreeMemoryBytes > stat.TotalMemoryBytes {
		stat.FreeMemoryBytes = stat.TotalMemoryBytes
	}
	return stat
}

// v2Dir resolves the cgroup v2 directory for this process: the unified
// hierarchy mount joined with the path from /proc/self/cgroup, falling
// back to the mount root when the joined path is not the directory holding
// the control files (the common case under a cgroup namespace). It reports
// false when no v2 control files are present.
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

// capacityV1 is the cgroup v1 capacity fallback for kernels without the
// unified hierarchy: the CPU quota over its period and the memory limit,
// read from the conventional controller mounts. Pressure sensing has no v1
// path -- an unmeasured dimension falls back to the host reading.
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

// cpuUsageCores derives the cgroup's recent CPU usage as a fraction of one
// core from the change in cpu.stat's cumulative usage_usec between two
// reads. The first read has no baseline and reports not-measured, so the
// caller keeps the host load for that cycle.
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

// usedMemory reports the cgroup's current memory charge from
// memory.current, and false when it cannot be read.
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

// readFile reads a root-relative path (joined onto the sensor root),
// returning the whole body; missing files read as the empty string.
func (s *containerSensor) readFile(rel string) string {
	return readWholeFile(filepath.Join(s.root, rel))
}

// readWhole reads an already-rooted absolute path, returning the whole
// body; missing files read as the empty string.
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

// readTrim reads an absolute control-file path (already rooted) and trims
// trailing whitespace; missing files read as the empty string.
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

// cgroupV2Path extracts the unified-hierarchy path from /proc/self/cgroup:
// the record whose hierarchy id is 0 and whose controller list is empty
// (the "0::<path>" line). It reports false when no v2 record is present.
func cgroupV2Path(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			return parts[2], true
		}
	}
	return "", false
}

// cgroupV1Path extracts the path for a v1 controller from
// /proc/self/cgroup: the record whose comma-separated controller list
// contains controller. It reports false when the controller is absent.
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

// parseCPUMax reads a cgroup v2 cpu.max value ("quota period", or "max
// period" for unbounded) into a core count. It is the inverse of
// [cpuMaxLine]. An unbounded or malformed value reports not-limited.
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

// cgroupUnlimitedMem is the floor at or above which a numeric cgroup memory
// limit is treated as unbounded: cgroup v1 writes a near-max sentinel
// rather than a word, and no real container is limited to exabytes.
const cgroupUnlimitedMem = uint64(1) << 62

// parseMemMax reads a cgroup memory limit ("max", the v1 near-max
// sentinel, or a byte count) into a byte count. An unbounded or malformed
// value reports not-limited.
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

// parseCpuset counts the CPUs a cgroup v2 cpuset list pins the process to
// ("0-3,6" -> 5), so a container narrowed by cpu affinity caps capacity even
// when cpu.max leaves the quota unbounded. An empty or malformed list reports
// not-limited.
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

// parseUsageUsec reads the cumulative usage_usec field from a cgroup v2
// cpu.stat body.
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
