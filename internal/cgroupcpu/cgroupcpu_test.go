package cgroupcpu

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLimitReadsACgroupV2Quota(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cpu.max"), "200000 100000\n")
	cores, ok := LimitUnder(dir)
	if !ok || cores != 2 {
		t.Fatalf("LimitUnder = %v, %v; want 2 cores", cores, ok)
	}
}

func TestLimitReadsACgroupV1Quota(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cpu", "cpu.cfs_quota_us"), "400000\n")
	writeFile(t, filepath.Join(dir, "cpu", "cpu.cfs_period_us"), "100000\n")
	cores, ok := LimitUnder(dir)
	if !ok || cores != 4 {
		t.Fatalf("LimitUnder = %v, %v; want 4 cores", cores, ok)
	}
}

// A container the kernel caps at nothing reports no limit, so a claim from a
// laptop prices its node by the node's own request.
func TestLimitReportsNothingWhenUncapped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cpu.max"), "max 100000\n")
	writeFile(t, filepath.Join(dir, "cpu", "cpu.cfs_quota_us"), "-1\n")
	writeFile(t, filepath.Join(dir, "cpu", "cpu.cfs_period_us"), "100000\n")
	if cores, ok := LimitUnder(dir); ok {
		t.Fatalf("LimitUnder = %v, true; want no limit", cores)
	}
	if cores, ok := LimitUnder(filepath.Join(dir, "absent")); ok {
		t.Fatalf("a missing cgroup tree reported %v cores", cores)
	}
}
