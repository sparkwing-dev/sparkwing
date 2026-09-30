//go:build darwin || linux

package nodemetrics

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeProcessCountersMatchGetrusage(t *testing.T) {
	before := checkedNativeCPU(t)
	consumeCPU(t, 150*time.Millisecond)
	after := checkedNativeCPU(t)
	measured := after.cpu - before.cpu
	if !before.valid || !after.valid || after.birth != before.birth {
		t.Fatalf("native measurements unavailable or identity changed: before=%+v after=%+v", before, after)
	}
	if measured < 150*time.Millisecond-nativeCPUPrecision(t) {
		t.Fatalf("native CPU delta %s omitted independently measured 150ms of work", measured)
	}
	t.Logf("native CPU delta %s; both reads inside independent OS brackets; RSS %d bytes", measured, after.rss)
}

func checkedNativeCPU(t *testing.T) processSample {
	t.Helper()
	var before, after unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := processSnapshot(os.Getpid())
	if !ok {
		t.Fatal("process snapshot unavailable")
	}
	p := snapshot[os.Getpid()]
	if err := unix.Getrusage(unix.RUSAGE_SELF, &after); err != nil {
		t.Fatal(err)
	}
	lower := time.Duration(before.Utime.Nano()+before.Stime.Nano()) - nativeCPUPrecision(t)
	upper := time.Duration(after.Utime.Nano()+after.Stime.Nano()) + 2*time.Microsecond
	if !p.valid || p.cpu < lower || p.cpu > upper {
		t.Fatalf("native CPU %s outside getrusage bracket [%s, %s]", p.cpu, lower, upper)
	}
	t.Logf("native CPU %s; independent bracket [%s, %s]", p.cpu, lower, upper)
	return p
}

func TestNativeResidentChild(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestNativeResidentChild$" {
		t.Skip("subprocess entry point")
	}
	allocation := make([]byte, 64<<20)
	for offset := 0; offset < len(allocation); offset += os.Getpagesize() {
		allocation[offset] = 1
	}
	consumeCPU(t, 150*time.Millisecond)
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	fmt.Println(usage.Utime.Nano() + usage.Stime.Nano())
	_, _ = io.Copy(io.Discard, os.Stdin)
	runtime.KeepAlive(allocation)
}

func consumeCPU(t *testing.T, target time.Duration) {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	start := usage.Utime.Nano() + usage.Stime.Nano()
	var value uint64 = 1
	for {
		for range 100_000 {
			value = value*1664525 + 1013904223
		}
		runtime.KeepAlive(value)
		if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		if time.Duration(usage.Utime.Nano()+usage.Stime.Nano()-start) >= target {
			return
		}
	}
}

func TestNativeTreeIncludesChildAndSeparatesReapedCPU(t *testing.T) {
	precision := nativeCPUPrecision(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeResidentChild$")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childCPUNanos, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if err != nil || childCPUNanos < int64(150*time.Millisecond) {
		t.Fatalf("child CPU oracle %q: %v", line, err)
	}
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var status syscall.WaitStatus
	// hack: Go's BSD Stopped method excludes SIGSTOP, so validate its wait-status encoding directly.
	stopped := syscall.WaitStatus(uint32(syscall.SIGSTOP)<<8 | 0x7f)
	if _, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WUNTRACED, nil); err != nil || status != stopped {
		t.Fatalf("stop child for resident-memory oracle: status=%v error=%v", status, err)
	}
	rssBefore := nativeRSSOracle(t, cmd.Process.Pid)
	snapshot, ok := processSnapshot(os.Getpid())
	if !ok {
		t.Fatal("process snapshot unavailable")
	}
	rssAfter := nativeRSSOracle(t, cmd.Process.Pid)
	child := snapshot[cmd.Process.Pid]
	if child.cpu < time.Duration(childCPUNanos)-precision {
		t.Fatalf("live child CPU %s omitted independently measured %dns", child.cpu, childCPUNanos)
	}
	_, treeMemory, treeOK := treeUsage(os.Getpid(), snapshot, snapshot)
	if !treeOK || !child.valid || child.parent != os.Getpid() || child.rss < min(rssBefore, rssAfter) || child.rss > max(rssBefore, rssAfter) || treeMemory != child.rss+snapshot[os.Getpid()].rss {
		t.Fatalf("child missing from native tree or outside independent RSS [%d, %d]: child=%+v tree=%+v", rssBefore, rssAfter, child, treeMemory)
	}
	beforeSnapshot, ok := processSnapshot(os.Getpid())
	if !ok {
		t.Fatal("pre-wait snapshot unavailable")
	}
	before := beforeSnapshot[os.Getpid()]
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	for !nativeZombieOracle(t, cmd.Process.Pid) {
		select {
		case <-ctx.Done():
			t.Fatal("child did not reach zombie state before deadline")
		default:
			runtime.Gosched()
		}
	}
	zombieSnapshot, ok := processSnapshot(os.Getpid())
	if !ok {
		t.Fatal("unreaped zombie snapshot unavailable")
	}
	zombie := zombieSnapshot[cmd.Process.Pid]
	if !zombie.valid || zombie.cpu < time.Duration(childCPUNanos)-precision || zombie.rss != 0 {
		t.Fatalf("unreaped child lost CPU or retained resident memory: %+v", zombie)
	}
	if zombieSnapshot[os.Getpid()].reaped != before.reaped {
		t.Fatal("child CPU reached parent counter before reap")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	afterSnapshot, ok := processSnapshot(os.Getpid())
	if !ok {
		t.Fatal("post-wait snapshot unavailable")
	}
	after := afterSnapshot[os.Getpid()]
	usage := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	oracle := time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	measured := after.reaped - before.reaped
	lower := time.Duration(childCPUNanos) - precision
	if !before.valid || !after.valid || measured < lower || measured > oracle+precision {
		t.Fatalf("native reaped CPU delta %s; wait4 child CPU %s; available=%t/%t", measured, oracle, before.valid, after.valid)
	}
	t.Logf("child RSS %d, OS oracle [%d, %d], tree RSS %d bytes; reaped CPU %s, independent bounds [%s, %s]", child.rss, rssBefore, rssAfter, treeMemory, measured, lower, oracle+precision)
}
