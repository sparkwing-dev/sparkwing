//go:build darwin || linux

package procusage

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
	measured := after.CPU - before.CPU
	if !before.CPUAvailable || !after.CPUAvailable || !after.RSSAvailable || !after.AncestryAvailable || after.Identity != before.Identity {
		t.Fatalf("native measurements unavailable or identity changed: before=%+v after=%+v", before, after)
	}
	if measured < 150*time.Millisecond-nativeCPUPrecision(t) {
		t.Fatalf("native CPU delta %s omitted independently measured 150ms of work", measured)
	}
	t.Logf("native CPU delta %s; both reads inside independent OS brackets; RSS %d bytes", measured, after.RSS)
}

func checkedNativeCPU(t *testing.T) Process {
	t.Helper()
	var before, after unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}
	p := (&Tree{PID: os.Getpid()}).Read().Processes[os.Getpid()]
	if err := unix.Getrusage(unix.RUSAGE_SELF, &after); err != nil {
		t.Fatal(err)
	}
	lower := time.Duration(before.Utime.Nano()+before.Stime.Nano()) - nativeCPUPrecision(t)
	upper := time.Duration(after.Utime.Nano()+after.Stime.Nano()) + 2*time.Microsecond
	if !p.CPUAvailable || p.CPU < lower || p.CPU > upper {
		t.Fatalf("native CPU %s outside getrusage bracket [%s, %s]", p.CPU, lower, upper)
	}
	t.Logf("native CPU %s; independent bracket [%s, %s]", p.CPU, lower, upper)
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
	snapshot := (&Tree{PID: os.Getpid()}).Read()
	rssAfter := nativeRSSOracle(t, cmd.Process.Pid)
	child := snapshot.Processes[cmd.Process.Pid]
	tree := Tree{PID: os.Getpid()}
	reading := tree.Observe(snapshot)
	if !child.CPUAvailable || !child.RSSAvailable || child.Parent != os.Getpid() || child.RSS < min(rssBefore, rssAfter) || child.RSS > max(rssBefore, rssAfter) || reading.RSSBytes != child.RSS+snapshot.Processes[os.Getpid()].RSS {
		t.Fatalf("child missing from native tree or outside independent RSS [%d, %d]: child=%+v tree=%+v", rssBefore, rssAfter, child, reading)
	}
	before := (&Tree{PID: os.Getpid()}).Read().Processes[os.Getpid()]
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	after := (&Tree{PID: os.Getpid()}).Read().Processes[os.Getpid()]
	usage := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	oracle := time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	measured := after.ReapedCPU - before.ReapedCPU
	lower := time.Duration(childCPUNanos) - nativeCPUPrecision(t)
	if !before.ReapedAvailable || !after.ReapedAvailable || measured < lower || measured > oracle+nativeCPUPrecision(t) {
		t.Fatalf("native reaped CPU delta %s; wait4 child CPU %s; available=%t/%t", measured, oracle, before.ReapedAvailable, after.ReapedAvailable)
	}
	t.Logf("child RSS %d, OS oracle [%d, %d], tree RSS %d bytes; reaped CPU %s, independent bounds [%s, %s]", child.RSS, rssBefore, rssAfter, reading.RSSBytes, measured, lower, oracle+nativeCPUPrecision(t))
}
