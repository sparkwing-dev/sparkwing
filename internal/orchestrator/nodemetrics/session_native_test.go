//go:build darwin || linux

package nodemetrics

import (
	"bufio"
	"context"
	"errors"
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

func TestSessionSnapshotOmitsReapedMember(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeResidentChild$")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	pid := cmd.Process.Pid
	sampled := false
	processes, ok := snapshotProcesses(func(all map[int]processSample) map[int]processSample {
		child, present := all[pid]
		if !present {
			t.Fatal("live child is absent before resident-memory sampling")
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		var exited *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &exited) {
			t.Fatalf("join killed child: %v", err)
		}
		sampled = true
		return map[int]processSample{pid: child, os.Getpid(): all[os.Getpid()]}
	})
	if !ok || !sampled || !processes[os.Getpid()].valid {
		t.Fatal("reaping another member invalidated the live process snapshot")
	}
	if _, present := processes[pid]; present {
		t.Fatal("snapshot retained a member reaped before resident-memory sampling")
	}
}

// safety: the node host's usage sampler reads only SessionProcesses, so the
// oracles judge that reader: this process's own CPU against getrusage, and a
// child's CPU, resident memory and reaped time against the kernel's own view
// of it, before and after wait4.
func sessionSelf(t *testing.T) (map[int]SessionProcess, SessionProcess) {
	t.Helper()
	sid, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	members, ok := SessionProcesses(sid)
	if !ok {
		t.Fatal("session snapshot unavailable")
	}
	self, ok := members[os.Getpid()]
	if !ok {
		t.Fatalf("session %d omits this process %d", sid, os.Getpid())
	}
	return members, self
}

func TestSessionProcessCPUMatchesGetrusage(t *testing.T) {
	cpuNow := func() time.Duration {
		var u unix.Rusage
		if err := unix.Getrusage(unix.RUSAGE_SELF, &u); err != nil {
			t.Fatal(err)
		}
		return time.Duration(u.Utime.Nano() + u.Stime.Nano())
	}
	sample := func() (SessionProcess, time.Duration, time.Duration) {
		lower := cpuNow()
		_, self := sessionSelf(t)
		return self, lower, cpuNow()
	}
	before, beforeLower, beforeUpper := sample()
	consumeCPU(t, 150*time.Millisecond)
	after, afterLower, afterUpper := sample()
	if after.Birth != before.Birth {
		t.Fatalf("identity changed between samples: %+v then %+v", before, after)
	}
	precision := nativeCPUPrecision(t)
	measured := after.CPU - before.CPU
	lower, upper := afterLower-beforeUpper-precision, afterUpper-beforeLower+precision
	if measured < lower || measured > upper {
		t.Fatalf("session CPU delta %s outside the getrusage bracket [%s, %s]", measured, lower, upper)
	}
	if measured < 150*time.Millisecond-precision || after.RSS <= 0 {
		t.Fatalf("session CPU delta %s or RSS %d misses the measured work", measured, after.RSS)
	}
}

func TestSessionProcessesCountALiveChildAZombieAndItsReap(t *testing.T) {
	precision := nativeCPUPrecision(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	pid := cmd.Process.Pid
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childNanos, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if err != nil || childNanos < int64(150*time.Millisecond) {
		t.Fatalf("child CPU oracle %q: %v", line, err)
	}
	childCPU := time.Duration(childNanos)
	// safety: a stopped child cannot fault in more pages, so the two kernel reads bracket the snapshot's RSS.
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var status syscall.WaitStatus
	stopped := syscall.WaitStatus(uint32(syscall.SIGSTOP)<<8 | 0x7f)
	if _, err := syscall.Wait4(pid, &status, syscall.WUNTRACED, nil); err != nil || status != stopped {
		t.Fatalf("stop the child: status=%v err=%v", status, err)
	}
	rssBefore := nativeRSSOracle(t, pid)
	live, _ := sessionSelf(t)
	rssAfter := nativeRSSOracle(t, pid)
	child, ok := live[pid]
	if !ok || child.Parent != os.Getpid() || child.CPU < childCPU-precision {
		t.Fatalf("live child %+v (present %v), want parent %d and at least %s of CPU", child, ok, os.Getpid(), childCPU)
	}
	if child.RSS < min(rssBefore, rssAfter) || child.RSS > max(rssBefore, rssAfter) {
		t.Fatalf("child RSS %d outside the kernel's [%d, %d]", child.RSS, rssBefore, rssAfter)
	}
	_, beforeReap := sessionSelf(t)
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	for !nativeZombieOracle(t, pid) {
		if ctx.Err() != nil {
			t.Fatal("the child never became a zombie")
		}
		runtime.Gosched()
	}
	zombies, self := sessionSelf(t)
	zombie, ok := zombies[pid]
	if !ok || zombie.CPU < childCPU-precision || zombie.RSS != 0 {
		t.Fatalf("unreaped child %+v (present %v), want its CPU kept and no resident memory", zombie, ok)
	}
	if self.Reaped != beforeReap.Reaped {
		t.Fatalf("reaped CPU moved from %s to %s before wait4", beforeReap.Reaped, self.Reaped)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	_, afterReap := sessionSelf(t)
	usage := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	oracle := time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	if got := afterReap.Reaped - beforeReap.Reaped; got < childCPU-precision || got > oracle+precision {
		t.Fatalf("reaped CPU delta %s, want between %s and wait4's %s", got, childCPU, oracle)
	}
}
