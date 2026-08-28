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

const subtreeSampleWindow = 500 * time.Millisecond

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

func TestCollectSubtree_ToleratesCycle(t *testing.T) {
	children := map[int][]int{1: {2}, 2: {1}}
	got := collectSubtree(1, children)
	sort.Ints(got)
	if !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("subtree = %v, want [1 2]", got)
	}
}

func TestProcSampler_CountsChildSubtreeCPU(t *testing.T) {
	requireObservableProcCPU(t)
	root := startProcessTree(t, `sh -c "while :; do :; done" & sleep 5`)
	p := newProcSampler()
	usage := sampleSubtreeCPU(t, p, root)
	if usage.Fraction <= 0.2 {
		t.Fatalf("subtree CPU credited to root = %.3f, want > 0.2 (busy descendant not counted)", usage.Fraction)
	}
}

func TestProcSampler_IdleTreeIsZero(t *testing.T) {
	requireObservableProcCPU(t)
	root := startProcessTree(t, `sleep 5 & wait`)
	p := newProcSampler()
	usage := sampleSubtreeCPU(t, p, root)
	if usage.Fraction > 0.1 {
		t.Fatalf("idle tree CPU = %.3f, want ~0", usage.Fraction)
	}
}

func sampleSubtreeCPU(t *testing.T, sampler *procSampler, root int) ProcUsage {
	t.Helper()
	sampler.CPUUsage(root)
	window := time.NewTimer(subtreeSampleWindow)
	defer window.Stop()
	<-window.C
	usage, ok := sampler.CPUUsage(root)
	if !ok {
		t.Fatalf("root pid %d produced no CPU sample", root)
	}
	if !usage.HasDescendant {
		t.Fatalf("root pid %d produced no descendant CPU sample", root)
	}
	return usage
}

func requireObservableProcCPU(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("per-process CPU not cheaply observable on %s", runtime.GOOS)
	}
}

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
	waitForProcessDescendant(t, pid)
	return pid
}

func waitForProcessDescendant(t *testing.T, root int) {
	t.Helper()
	sampler := newProcSampler()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		usage, ok := sampler.CPUUsage(root)
		if ok && usage.HasDescendant {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("root pid %d did not expose a descendant before the deadline", root)
		}
	}
}
