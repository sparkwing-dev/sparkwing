package wingd_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	fixtureOnce sync.Once
	fixtureBin  string
	fixtureErr  error
)

// buildFixture compiles the testprog helper once per test binary.
func buildFixture(t *testing.T) string {
	t.Helper()
	fixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("/tmp", "wdfix")
		if err != nil {
			fixtureErr = err
			return
		}
		bin := filepath.Join(dir, "testprog")
		cmd := exec.Command("go", "build", "-o", bin,
			"github.com/sparkwing-dev/sparkwing/internal/wingd/testprog")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fixtureErr = err
			return
		}
		fixtureBin = bin
	})
	if fixtureErr != nil {
		t.Fatalf("build fixture: %v", fixtureErr)
	}
	return fixtureBin
}

type procHandle struct {
	t     *testing.T
	cmd   *exec.Cmd
	lines chan string
}

func startProc(t *testing.T, args ...string) *procHandle {
	t.Helper()
	bin := buildFixture(t)
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	ph := &procHandle{t: t, cmd: cmd, lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			ph.lines <- sc.Text()
		}
		close(ph.lines)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return ph
}

func (ph *procHandle) waitOK(timeout time.Duration) string {
	ph.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatal("process exited before reporting OK")
			}
			if tok, found := strings.CutPrefix(line, "OK "); found {
				return tok
			}
		case <-deadline:
			ph.t.Fatal("timed out waiting for OK")
		}
	}
}

func (ph *procHandle) mustStayQueued(within time.Duration) {
	ph.t.Helper()
	select {
	case line, ok := <-ph.lines:
		if ok && strings.HasPrefix(line, "OK ") {
			ph.t.Fatalf("process was admitted early: %q", line)
		}
	case <-time.After(within):
	}
}

func (ph *procHandle) kill(sig syscall.Signal) {
	_ = ph.cmd.Process.Signal(sig)
}

func readDaemonPid(t *testing.T, home string) int {
	t.Helper()
	path := filepath.Join(home, "wingd", "daemons.log")
	var last string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Fields(strings.TrimSpace(string(data)))
			if len(lines) > 0 {
				last = lines[len(lines)-1]
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last == "" {
		t.Fatal("no daemon pid recorded")
	}
	pid, err := strconv.Atoi(last)
	if err != nil {
		t.Fatalf("parse daemon pid %q: %v", last, err)
	}
	return pid
}

func daemonLineCount(t *testing.T, home string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "wingd", "daemons.log"))
	if err != nil {
		t.Fatalf("read daemons.log: %v", err)
	}
	return len(strings.Fields(strings.TrimSpace(string(data))))
}

// TestProcess_ElectionRaceSingleDaemon starts several client processes at
// once, each of which will spawn a daemon if none is running; the flock
// election must leave exactly one daemon serving all of them.
func TestProcess_ElectionRaceSingleDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	const n = 6
	holders := make([]*procHandle, n)
	for i := range holders {
		holders[i] = startProc(t, "hold",
			"--home", home,
			"--run", "r"+strconv.Itoa(i),
			"--cores", "0.1",
			"--daemon-idle-ms", "1500",
		)
	}
	for i, h := range holders {
		if tok := h.waitOK(10 * time.Second); tok == "" {
			t.Fatalf("holder %d got empty token", i)
		}
	}
	if got := daemonLineCount(t, home); got != 1 {
		t.Fatalf("election left %d daemons serving, want exactly 1", got)
	}
}

// TestProcess_ClientKillReleasesAndPromotes SIGKILLs a lease holder and
// asserts the queued waiter is promoted -- the kernel closing the socket
// is the only liveness signal.
func TestProcess_ClientKillReleasesAndPromotes(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	a := startProc(t, "hold", "--home", home, "--run", "a", "--sem", "lock", "--daemon-idle-ms", "3000")
	a.waitOK(10 * time.Second)

	b := startProc(t, "hold", "--home", home, "--run", "b", "--sem", "lock", "--daemon-idle-ms", "3000")
	b.mustStayQueued(500 * time.Millisecond)

	a.kill(syscall.SIGKILL)
	b.waitOK(5 * time.Second)
}

// TestProcess_DaemonKillRestoresAndReattaches SIGKILLs the daemon, then a
// client reclaims its surviving lease from a fresh daemon within the
// grace window.
func TestProcess_DaemonKillRestoresAndReattaches(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	a := startProc(t, "hold", "--home", home, "--run", "a", "--cores", "0.5",
		"--daemon-grace-ms", "4000", "--daemon-idle-ms", "3000")
	token := a.waitOK(10 * time.Second)

	dpid := readDaemonPid(t, home)
	if err := syscall.Kill(dpid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill daemon %d: %v", dpid, err)
	}

	r := startProc(t, "reattach", "--home", home, "--token", token,
		"--daemon-grace-ms", "4000", "--daemon-idle-ms", "3000")
	reclaimed := r.waitOK(10 * time.Second)
	if reclaimed != token {
		t.Fatalf("reattached token %q, want %q", reclaimed, token)
	}
}
