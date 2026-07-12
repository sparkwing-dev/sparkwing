package wingd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCgroupV2 lays a cgroup v2 fixture tree under a fresh root: a
// /proc/self/cgroup pointing at "/" and the given control files directly
// under the unified mount. An empty file value is omitted.
func writeCgroupV2(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "proc", "self", "cgroup"), "0::/\n")
	base := filepath.Join(root, "sys", "fs", "cgroup")
	for name, body := range files {
		if body == "" {
			continue
		}
		mustWrite(t, filepath.Join(base, name), body)
	}
	return root
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestParseCPUMax(t *testing.T) {
	tests := []struct {
		in    string
		cores float64
		ok    bool
	}{
		{"600000 100000", 6, true},
		{"50000 100000", 0.5, true},
		{"200000 100000\n", 2, true},
		{"max 100000", 0, false},
		{"", 0, false},
		{"garbage", 0, false},
	}
	for _, tc := range tests {
		cores, ok := parseCPUMax(tc.in)
		if ok != tc.ok || cores != tc.cores {
			t.Errorf("parseCPUMax(%q) = %v,%v want %v,%v", tc.in, cores, ok, tc.cores, tc.ok)
		}
	}
}

func TestParseMemMax(t *testing.T) {
	tests := []struct {
		in    string
		bytes uint64
		ok    bool
	}{
		{"6442450944", 6 << 30, true},
		{"6442450944\n", 6 << 30, true},
		{"max", 0, false},
		{"", 0, false},
		{"9223372036854771712", 0, false},
	}
	for _, tc := range tests {
		b, ok := parseMemMax(tc.in)
		if ok != tc.ok || b != tc.bytes {
			t.Errorf("parseMemMax(%q) = %d,%v want %d,%v", tc.in, b, ok, tc.bytes, tc.ok)
		}
	}
}

func TestContainerSensor_CapacityLimits_V2(t *testing.T) {
	root := writeCgroupV2(t, map[string]string{
		"cpu.max":    "600000 100000",
		"memory.max": "6442450944",
	})
	cores, mem := newContainerSensor(root).capacityLimits()
	if cores != 6 || mem != 6<<30 {
		t.Fatalf("capacityLimits = %v,%d want 6,%d", cores, mem, uint64(6)<<30)
	}
}

func TestContainerSensor_CapacityLimits_Unlimited(t *testing.T) {
	root := writeCgroupV2(t, map[string]string{
		"cpu.max":    "max 100000",
		"memory.max": "max",
	})
	cores, mem := newContainerSensor(root).capacityLimits()
	if cores != 0 || mem != 0 {
		t.Fatalf("capacityLimits = %v,%d want 0,0 (unlimited)", cores, mem)
	}
}

func TestContainerSensor_CapacityLimits_NoCgroup(t *testing.T) {
	cores, mem := newContainerSensor(t.TempDir()).capacityLimits()
	if cores != 0 || mem != 0 {
		t.Fatalf("capacityLimits = %v,%d want 0,0 (absent)", cores, mem)
	}
}

func TestContainerSensor_CapacityLimits_NestedPath(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "proc", "self", "cgroup"), "0::/system.slice/run.scope\n")
	dir := filepath.Join(root, "sys", "fs", "cgroup", "system.slice", "run.scope")
	mustWrite(t, filepath.Join(dir, "cpu.max"), "400000 100000")
	mustWrite(t, filepath.Join(dir, "memory.max"), "max")
	cores, mem := newContainerSensor(root).capacityLimits()
	if cores != 4 || mem != 0 {
		t.Fatalf("capacityLimits = %v,%d want 4,0", cores, mem)
	}
}

func TestContainerSensor_CapacityLimits_V1Fallback(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "proc", "self", "cgroup"),
		"5:memory:/docker/abc\n4:cpu,cpuacct:/docker/abc\n")
	memDir := filepath.Join(root, "sys", "fs", "cgroup", "memory", "docker", "abc")
	mustWrite(t, filepath.Join(memDir, "memory.limit_in_bytes"), "3221225472")
	cpuDir := filepath.Join(root, "sys", "fs", "cgroup", "cpu,cpuacct", "docker", "abc")
	mustWrite(t, filepath.Join(cpuDir, "cpu.cfs_quota_us"), "300000")
	mustWrite(t, filepath.Join(cpuDir, "cpu.cfs_period_us"), "100000")
	cores, mem := newContainerSensor(root).capacityLimits()
	if cores != 3 || mem != 3<<30 {
		t.Fatalf("v1 capacityLimits = %v,%d want 3,%d", cores, mem, uint64(3)<<30)
	}
}

func TestContainerSensor_Apply_ClampsBelowHost(t *testing.T) {
	root := writeCgroupV2(t, map[string]string{
		"cpu.max":        "600000 100000",
		"memory.max":     "6442450944",
		"memory.current": "2147483648",
	})
	host := HostStat{TotalCores: 24, TotalMemoryBytes: 24 << 30, LoadAverage: 3, FreeMemoryBytes: 20 << 30}
	got := newContainerSensor(root).apply(host)
	if got.TotalCores != 6 {
		t.Errorf("TotalCores = %v want 6", got.TotalCores)
	}
	if got.TotalMemoryBytes != 6<<30 {
		t.Errorf("TotalMemoryBytes = %d want %d", got.TotalMemoryBytes, uint64(6)<<30)
	}
	if want := uint64(6<<30) - uint64(2<<30); got.FreeMemoryBytes != want {
		t.Errorf("FreeMemoryBytes = %d want %d (max - current)", got.FreeMemoryBytes, want)
	}
}

func TestContainerSensor_Apply_UnlimitedLeavesHost(t *testing.T) {
	root := writeCgroupV2(t, map[string]string{
		"cpu.max":    "max 100000",
		"memory.max": "max",
	})
	host := HostStat{TotalCores: 8, TotalMemoryBytes: 8 << 30, LoadAverage: 2, FreeMemoryBytes: 5 << 30}
	got := newContainerSensor(root).apply(host)
	if got != host {
		t.Fatalf("apply with unlimited cgroup = %+v want %+v (unchanged)", got, host)
	}
}

func TestContainerSensor_Apply_LargerLimitLeavesHost(t *testing.T) {
	root := writeCgroupV2(t, map[string]string{
		"cpu.max":    "3200000 100000",
		"memory.max": "34359738368",
	})
	host := HostStat{TotalCores: 8, TotalMemoryBytes: 8 << 30, LoadAverage: 1, FreeMemoryBytes: 6 << 30}
	got := newContainerSensor(root).apply(host)
	if got != host {
		t.Fatalf("apply with over-host cgroup = %+v want %+v (unchanged)", got, host)
	}
}

func TestContainerSensor_Apply_CPUUsageRate(t *testing.T) {
	root := writeCgroupV2(t, map[string]string{
		"cpu.max":    "600000 100000",
		"memory.max": "6442450944",
		"cpu.stat":   "usage_usec 1000000\n",
	})
	s := newContainerSensor(root)
	base := time.Unix(0, 0)
	s.now = func() time.Time { return base }
	host := HostStat{TotalCores: 24, TotalMemoryBytes: 24 << 30, LoadAverage: 20}
	first := s.apply(host)
	if first.LoadAverage != 20 {
		t.Fatalf("first apply LoadAverage = %v want host 20 (no baseline)", first.LoadAverage)
	}
	mustWrite(t, filepath.Join(root, "sys", "fs", "cgroup", "cpu.stat"), "usage_usec 5000000\n")
	s.now = func() time.Time { return base.Add(2 * time.Second) }
	second := s.apply(host)
	if second.LoadAverage != 2 {
		t.Fatalf("second apply LoadAverage = %v want 2 cores (4s cpu over 2s wall)", second.LoadAverage)
	}
}

func TestContainerSensor_Nil(t *testing.T) {
	var s *containerSensor
	host := HostStat{TotalCores: 8, TotalMemoryBytes: 8 << 30}
	if got := s.apply(host); got != host {
		t.Errorf("nil sensor apply = %+v want %+v", got, host)
	}
	if cores, mem := s.capacityLimits(); cores != 0 || mem != 0 {
		t.Errorf("nil sensor capacityLimits = %v,%d want 0,0", cores, mem)
	}
}

func TestContainerSensorFor_Gate(t *testing.T) {
	if s := containerSensorFor(Config{Sampler: fakeHostSampler{}}); s != nil {
		t.Error("injected sampler without ContainerRoot should disable detection")
	}
	if s := containerSensorFor(Config{}); s == nil {
		t.Error("real platform sampler (nil Sampler) should enable detection")
	}
	if s := containerSensorFor(Config{Sampler: fakeHostSampler{}, ContainerRoot: t.TempDir()}); s == nil {
		t.Error("explicit ContainerRoot should enable detection")
	}
}

// fakeHostSampler is a stand-in host reading for gate tests; the value is
// never sampled.
type fakeHostSampler struct{}

func (fakeHostSampler) Sample() (HostStat, error) { return HostStat{}, nil }
