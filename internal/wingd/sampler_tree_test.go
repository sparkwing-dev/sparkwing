//go:build linux || darwin

package wingd

import (
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"syscall"
	"testing"
	"time"
)

// TestCollectSubtree_GathersEveryDescendant is the platform-independent
// core of the fix: a holder's forked work must be reachable from its pid
// through the parent->children map, several levels deep, while an
// unrelated tree stays out.
func TestCollectSubtree_GathersEveryDescendant(t *testing.T) {
	children := map[int][]int{
		10: {11, 12},
		11: {13},
		13: {14},
		12: {15},
		99: {98},
	}
	got := collectSubtree(10, children)
	sort.Ints(got)
	want := []int{10, 11, 12, 13, 14, 15}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subtree = %v, want %v", got, want)
	}
}

// TestCollectSubtree_ToleratesCycle guards against a hang when recycled
// pids make the parent map circular.
func TestCollectSubtree_ToleratesCycle(t *testing.T) {
	children := map[int][]int{1: {2}, 2: {1}}
	got := collectSubtree(1, children)
	sort.Ints(got)
	if !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("subtree = %v, want [1 2]", got)
	}
}

// TestProcSampler_CountsChildSubtreeCPU spawns a holder whose own process
// only waits while a busy loop burns a core in a descendant, and asserts
// the sampler credits that descendant's CPU to the holder pid. A sampler
// that read only the holder's own pid would report near-idle.
func TestProcSampler_CountsChildSubtreeCPU(t *testing.T) {
	requireObservableProcCPU(t)
	root := startProcessTree(t, `sh -c "while :; do :; done" & sleep 5`)
	p := newProcSampler()

	p.CPUUsage(root)
	time.Sleep(500 * time.Millisecond)
	usage, ok := p.CPUUsage(root)
	if !ok {
		t.Fatalf("root pid %d not sampled", root)
	}
	if usage.Fraction <= 0.2 {
		t.Fatalf("subtree CPU credited to root = %.3f, want > 0.2 (busy descendant not counted)", usage.Fraction)
	}
	if !usage.HasDescendant {
		t.Fatalf("root pid %d has a forked child, want HasDescendant", root)
	}
}

// TestProcSampler_IdleTreeIsZero spawns a holder whose whole tree only
// sleeps, and asserts the sampler reports it near-idle -- the tree walk
// must not manufacture CPU where none is spent.
func TestProcSampler_IdleTreeIsZero(t *testing.T) {
	requireObservableProcCPU(t)
	root := startProcessTree(t, `sleep 5`)
	p := newProcSampler()

	p.CPUUsage(root)
	time.Sleep(500 * time.Millisecond)
	usage, ok := p.CPUUsage(root)
	if !ok {
		t.Fatalf("root pid %d not sampled", root)
	}
	if usage.Fraction > 0.1 {
		t.Fatalf("idle tree CPU = %.3f, want ~0", usage.Fraction)
	}
}

// requireObservableProcCPU skips where per-process CPU cannot be read
// cheaply. macOS kinfo_proc P_pctcpu is unmaintained on current releases
// -- it reads zero even for a pegged process -- so the magnitude
// assertions only hold on Linux, where /proc exposes cumulative CPU.
func requireObservableProcCPU(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("per-process CPU not cheaply observable on %s", runtime.GOOS)
	}
}

// startProcessTree launches "sh -c script" in its own process group --
// mirroring how sparkwing runs each command -- so the busy work lives in
// a descendant the holder pid never touches, proving the sampler walks
// the tree rather than grouping by pgid. It gives the backgrounded work a
// moment to spin up and kills the whole group on cleanup.
func startProcessTree(t *testing.T, script string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process tree: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		syscall.Kill(-pid, syscall.SIGKILL)
		cmd.Wait()
	})
	time.Sleep(400 * time.Millisecond)
	return pid
}
