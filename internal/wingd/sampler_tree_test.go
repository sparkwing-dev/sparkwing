//go:build linux || darwin

package wingd

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"syscall"
	"testing"
	"time"
)

const subtreeSampleWindow = 500 * time.Millisecond

const subtreeCPUWork = 150 * time.Millisecond

// safety: Linux process counters quantize both endpoints to 10ms ticks.
const subtreeCPUPrecision = 20 * time.Millisecond

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
	if testing.Short() {
		t.Skip("slow: starts a CPU-bound descendant; the fast class runs under -short")
	}
	requireObservableProcCPU(t)
	// safety: A background shell command starts with /dev/null on stdin.
	cmd := exec.Command("sh", "-c", `exec 3<&0; "$0" -test.run=^TestProcSamplerCPUChild$ <&3 & wait`, os.Args[0])
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	root := startProcessTree(t, cmd)
	reader := bufio.NewReader(stdout)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness = %q, %v", line, err)
	}
	p := newProcSampler()
	p.CPUUsage(root)
	before := p.last[root].at
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var childCPU int64
	if _, err := fmt.Fscanln(reader, &childCPU); err != nil || childCPU < int64(subtreeCPUWork) {
		t.Fatalf("child CPU oracle = %d, %v", childCPU, err)
	}
	usage, ok := p.CPUUsage(root)
	if !ok || !usage.HasDescendant {
		t.Fatalf("root pid %d produced no descendant CPU sample", root)
	}
	credited := usage.Fraction * p.last[root].at.Sub(before).Seconds()
	minimum := (time.Duration(childCPU) - subtreeCPUPrecision).Seconds()
	if credited < minimum {
		t.Fatalf("subtree CPU credited %.3fs, want at least %.3fs from child oracle", credited, minimum)
	}
}

func TestProcSamplerCPUChild(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestProcSamplerCPUChild$" {
		t.Skip("subprocess entry point")
	}
	fmt.Println("ready")
	var signal [1]byte
	if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
		t.Fatal(err)
	}
	var before, after syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}
	var cpu int64
	for cpu < int64(subtreeCPUWork) {
		value := uint64(1)
		for range 100_000 {
			value = (value << 1) ^ (value*17 + 3)
		}
		runtime.KeepAlive(value)
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
			t.Fatal(err)
		}
		cpu = after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()
	}
	fmt.Println(cpu)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestProcSampler_IdleTreeIsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	requireObservableProcCPU(t)
	root := startProcessTree(t, exec.Command("sh", "-c", `sleep 5 & wait`))
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

func startProcessTree(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
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
