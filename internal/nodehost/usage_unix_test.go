//go:build unix

package nodehost

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
)

const orphanBytes = 128 << 20

// safety: on Linux a process starts with the peak RSS of the one that forked
// it, so the leader's peak can be the test binary's; the orphan allocates
// that much more than the test binary has ever held, so only it can reach it.
func orphanAllocation() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return orphanBytes
	}
	self := ru.Maxrss
	if runtime.GOOS == "linux" {
		self *= 1024
	}
	return self + orphanBytes
}

func helper(role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestUsageHelperProcess$")
	cmd.Env = append(os.Environ(), "SW_USAGE_HELPER="+role)
	cmd.Stdout = os.Stdout
	return cmd
}

func burnCPU(d time.Duration) {
	for {
		var ru syscall.Rusage
		if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
			os.Exit(3)
		}
		if time.Duration(ru.Utime.Nano()+ru.Stime.Nano()) >= d {
			return
		}
	}
}

// The leader starts a parent, which starts a grandchild it waits for and an
// orphan it abandons. The orphan's time and memory never reach the leader's
// rusage, so only a session-wide sampler sees them. Every helper holds the
// test's stdout pipe, so its EOF means the whole tree has exited.
func TestUsageHelperProcess(t *testing.T) {
	switch os.Getenv("SW_USAGE_HELPER") {
	case "":
		return
	case "leader":
		if helper("parent").Run() != nil {
			os.Exit(2)
		}
	case "parent":
		small, orphan := helper("small"), helper("orphan")
		// safety: the orphan reads this pipe to EOF, which arrives when this
		// process exits, so it runs only after being reparented.
		_, pipeErr := orphan.StdinPipe()
		if pipeErr != nil || small.Start() != nil || orphan.Start() != nil || small.Wait() != nil {
			os.Exit(2)
		}
	case "small":
		burnCPU(200 * time.Millisecond)
	case "orphan":
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(2)
		}
		size, err := strconv.ParseInt(os.Getenv("SW_USAGE_ORPHAN_BYTES"), 10, 64)
		if err != nil {
			os.Exit(2)
		}
		memory := make([]byte, size)
		for i := 0; i < len(memory); i += os.Getpagesize() {
			memory[i] = 1
		}
		burnCPU(500 * time.Millisecond)
		runtime.KeepAlive(memory)
	}
	os.Exit(0)
}

func TestUsageMeasuresWholeSession(t *testing.T) {
	defer nodemetrics.SetIntervalForTest(20 * time.Millisecond)()
	tree, treeOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	alloc := orphanAllocation()
	cmd := helper("leader")
	cmd.Env = append(cmd.Env, "SW_USAGE_ORPHAN_BYTES="+strconv.FormatInt(alloc, 10))
	cmd.Stdout = treeOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	treeOut.Close()
	if err != nil {
		t.Fatal(err)
	}
	sampler := StartUsage(cmd.Process.Pid)
	if _, err := io.Copy(io.Discard, tree); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	u := sampler.Stop(cmd.ProcessState)

	leaderCPU := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
	if !u.SessionWide || u.CPUTime < leaderCPU+400*time.Millisecond {
		t.Fatalf("usage = %+v, want session-wide CPU of at least leader %v plus the orphan's 500ms", u, leaderCPU)
	}
	if leaderRSS := maxRSSBytes(cmd.ProcessState); leaderRSS >= alloc || u.MaxRSSBytes < alloc {
		t.Fatalf("usage = %+v, leader peak %d; want the orphan's %d only in the session peak", u, leaderRSS, alloc)
	}
}
