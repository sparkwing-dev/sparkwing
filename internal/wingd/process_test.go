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
	return startProcEnv(t, nil, args...)
}

// startProcEnv is [startProc] with extra environment entries appended to
// the inherited environment, for the paths whose behavior is decided by
// the environment the process is launched with.
func startProcEnv(t *testing.T, extraEnv []string, args ...string) *procHandle {
	t.Helper()
	bin := buildFixture(t)
	cmd := exec.Command(bin, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
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

func (ph *procHandle) waitLine(prefix string, timeout time.Duration) string {
	ph.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatalf("process exited before printing %q", prefix)
			}
			if rest, found := strings.CutPrefix(line, prefix); found {
				return strings.TrimSpace(rest)
			}
		case <-deadline:
			ph.t.Fatalf("timed out waiting for %q", prefix)
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
	data := waitForNonemptyFile(t, path, 3*time.Second, 50*time.Millisecond)
	lines := strings.Fields(strings.TrimSpace(string(data)))
	last := lines[len(lines)-1]
	pid, err := strconv.Atoi(last)
	if err != nil {
		t.Fatalf("parse daemon pid %q: %v", last, err)
	}
	return pid
}

func waitForNonemptyFile(t *testing.T, path string, timeout, interval time.Duration) []byte {
	t.Helper()
	poll := time.NewTicker(interval)
	defer poll.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", path, err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("%s remained empty for %s", path, timeout)
		}
	}
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
			"--sem", "election-"+strconv.Itoa(i),
			"--semaphores-only",
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

// TestProcess_CancelOthersEvictsHolderAcrossProcesses starts two real
// processes contending on a capacity-1 semaphore under cancel_others: the
// newer arrival must evict the older holder (newest wins), the holder
// must observe the eviction naming the contested key and superseding run,
// and the aggressor must be admitted -- never queued behind the holder.
func TestProcess_CancelOthersEvictsHolderAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	victim := startProc(t, "hold", "--home", home, "--run", "victim",
		"--sem", "lock", "--sem-policy", "cancel_others", "--semaphores-only",
		"--cancel-timeout-ms", "5000", "--daemon-idle-ms", "3000")
	victim.waitOK(10 * time.Second)

	aggressor := startProc(t, "hold", "--home", home, "--run", "aggressor",
		"--sem", "lock", "--sem-policy", "cancel_others", "--semaphores-only",
		"--cancel-timeout-ms", "5000", "--daemon-idle-ms", "3000")
	aggressor.waitOK(10 * time.Second)

	got := victim.waitLine("EVICTED ", 10*time.Second)
	if !strings.Contains(got, "lock") || !strings.Contains(got, "aggressor") {
		t.Fatalf("victim eviction = %q, want the contested key and superseding run named", got)
	}
}

// TestProcess_SelfSpawnedDaemonWritesLogFile exercises the real default
// spawn path (no injected spawner): a client that finds no daemon
// re-execs the binary as `wingd run`, which must reliably create the
// documented log file and record its history there -- the log is the only
// witness when the invisible daemon misbehaves.
func TestProcess_SelfSpawnedDaemonWritesLogFile(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	h := startProc(t, "hold", "--home", home, "--run", "r", "--cores", "0.1", "--real-spawn")
	h.waitOK(10 * time.Second)

	logPath := filepath.Join(home, "wingd", "d.log")
	data := waitForNonemptyFile(t, logPath, 5*time.Second, 20*time.Millisecond)
	if !strings.Contains(string(data), "wingd:") || !strings.Contains(string(data), "elected") {
		t.Fatalf("daemon log lacks a meaningful history line:\n%s", data)
	}
}

// TestProcess_PipelineClientStartsDaemonViaHostBinary exercises the one
// spawn path a non-hosting client retains. A compiled pipeline binary
// invoked directly -- a systemd unit, a deploy box, no CLI in the loop --
// with $SPARKWING_WINGD_BIN naming an installed sparkwing must bring the
// daemon up by starting *that* binary, never by re-execing itself.
//
// The daemon's advertised version is the proof: the host binary names its
// own build, and the spawn deliberately drops the client's, so a daemon
// reporting anything but the host's default would mean the client hosted
// itself after all.
func TestProcess_PipelineClientStartsDaemonViaHostBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	hostBin := buildFixture(t)
	env := []string{
		"SPARKWING_WINGD_BIN=" + hostBin,
		// An empty PATH proves the env var is what resolved the host: with
		// a sparkwing on the developer's PATH the fallback would pass this
		// test for the wrong reason.
		"PATH=" + t.TempDir(),
	}
	h := startProcEnv(t, env, "hold",
		"--home", home,
		"--run", "hosted",
		"--cores", "0.1",
		"--host-spawn",
		"--version", "v1.0.0",
	)
	if got := h.waitLine("DAEMON ", 20*time.Second); got != "v1.0.0" {
		t.Fatalf("daemon version = %q, want v1.0.0 -- the daemon is not the binary $SPARKWING_WINGD_BIN named", got)
	}
	if tok := h.waitOK(10 * time.Second); tok == "" {
		t.Fatal("host-spawned daemon granted an empty lease token")
	}
}

// TestProcess_PipelineClientWithoutAHostStartsNothing is the other half:
// with no host binary resolvable, a non-hosting client must report the
// absence with its sentinel rather than fall back to hosting itself. A
// caller that can run uncoordinated keys off exactly that sentinel.
func TestProcess_PipelineClientWithoutAHostStartsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	env := []string{"SPARKWING_WINGD_BIN=", "PATH=" + t.TempDir()}
	h := startProcEnv(t, env, "hold",
		"--home", home, "--run", "hostless", "--cores", "0.1", "--host-spawn")
	got := h.waitLine("ENSURE-ERR ", 20*time.Second)
	if !strings.Contains(got, "no sparkwing binary is available to host one") {
		t.Fatalf("error = %q, want the no-host sentinel", got)
	}
	if _, err := os.Stat(filepath.Join(home, "wingd", "d.log")); err == nil {
		t.Fatal("a client with no host binary started a daemon anyway")
	}
}

// TestProcess_DaemonKillRestoresAndReattaches SIGKILLs the daemon and proves
// the surviving holder reclaims its lease from the successor. A second token
// claimant would compete with the holder rather than exercise holder recovery.
func TestProcess_DaemonKillRestoresAndReattaches(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	a := startProc(t, "hold", "--home", home, "--run", "a", "--cores", "0.5",
		"--daemon-grace-ms", "500", "--daemon-idle-ms", "3000")
	a.waitOK(10 * time.Second)

	dpid := readDaemonPid(t, home)
	if err := syscall.Kill(dpid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill daemon %d: %v", dpid, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for daemonLineCount(t, home) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if daemonLineCount(t, home) < 2 {
		t.Fatal("successor daemon was not elected")
	}

	// A restored lease that was not reclaimed would expire after this window.
	time.Sleep(750 * time.Millisecond)
	if err := a.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("original holder exited instead of reclaiming its lease: %v", err)
	}
	waitForHolder(t, home, "a")
}
