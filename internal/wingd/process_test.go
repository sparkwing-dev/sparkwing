package wingd_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

var (
	fixtureOnce sync.Once
	fixtureDir  string
	fixtureBin  string
	fixtureErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(fixtureDir)
	if code == 0 {
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

func buildFixture(t *testing.T) string {
	t.Helper()
	fixtureOnce.Do(func() {
		dir, err := os.MkdirTemp(processFixtureTempRoot(), "wdfix")
		if err != nil {
			fixtureErr = err
			return
		}
		fixtureDir = dir
		bin := filepath.Join(dir, "testprog"+processFixtureSuffix())
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
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatal("process exited before reporting OK")
			}
			if tok, found := strings.CutPrefix(line, "OK "); found {
				return tok
			}
		case <-timer.C:
			ph.t.Fatal("timed out waiting for OK")
		}
	}
}

func (ph *procHandle) waitLine(prefix string, timeout time.Duration) string {
	ph.t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatalf("process exited before printing %q", prefix)
			}
			if rest, found := strings.CutPrefix(line, prefix); found {
				return strings.TrimSpace(rest)
			}
		case <-timer.C:
			ph.t.Fatalf("timed out waiting for %q", prefix)
		}
	}
}

func (ph *procHandle) mustStayQueued(within time.Duration) {
	ph.t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatal("process exited while expected to remain queued")
			}
			if strings.HasPrefix(line, "OK ") {
				ph.t.Fatalf("process was admitted early: %q", line)
			}
		case <-timer.C:
			return
		}
	}
}

func (ph *procHandle) kill() {
	_ = ph.cmd.Process.Kill()
}

func readDaemonPid(t *testing.T, home string) int {
	t.Helper()
	path := filepath.Join(home, "wingd", "daemons.log")
	data := waitForNonblankFile(t, path, 3*time.Second, 50*time.Millisecond)
	lines := strings.Fields(strings.TrimSpace(string(data)))
	last := lines[len(lines)-1]
	pid, err := strconv.Atoi(last)
	if err != nil {
		t.Fatalf("parse daemon pid %q: %v", last, err)
	}
	return pid
}

func waitForNonblankFile(t *testing.T, path string, timeout, interval time.Duration) []byte {
	t.Helper()
	poll := time.NewTicker(interval)
	defer poll.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return data
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", path, err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("%s remained blank for %s", path, timeout)
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

func waitForDaemonLineCount(t *testing.T, home string, want int, timeout time.Duration) {
	t.Helper()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if got := daemonLineCount(t, home); got >= want {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("daemon history contains fewer than %d entries after %s", want, timeout)
		}
	}
}

func TestProcess_ElectionRaceSingleDaemon(t *testing.T) {
	t.Parallel()

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

func TestProcess_ClientKillReleasesAndPromotes(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	a := startProc(t, "hold", "--home", home, "--run", "a", "--sem", "lock", "--daemon-idle-ms", "3000")
	a.waitOK(10 * time.Second)

	b := startProc(t, "hold", "--home", home, "--run", "b", "--sem", "lock", "--daemon-idle-ms", "3000")
	b.mustStayQueued(500 * time.Millisecond)

	a.kill()
	b.waitOK(5 * time.Second)
}

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

func TestProcess_SelfSpawnedDaemonWritesLogFile(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	h := startProc(t, "hold", "--home", home, "--run", "r", "--cores", "0.1", "--real-spawn")
	h.waitOK(10 * time.Second)

	logPath := filepath.Join(home, "wingd", "d.log")
	data := waitForNonblankFile(t, logPath, 5*time.Second, 20*time.Millisecond)
	if !strings.Contains(string(data), "wingd:") || !strings.Contains(string(data), "elected") {
		t.Fatalf("daemon log lacks a meaningful history line:\n%s", data)
	}
}

func TestProcess_PipelineClientStartsDaemonViaHostBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	hostBin := buildFixture(t)
	env := []string{
		"SPARKWING_WINGD_BIN=" + hostBin,

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

func TestProcess_DaemonKillRestoresAndReattaches(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	a := startProc(t, "hold", "--home", home, "--run", "a", "--cores", "0.5",
		"--daemon-grace-ms", "500", "--daemon-idle-ms", "3000")
	a.waitOK(10 * time.Second)

	dpid := readDaemonPid(t, home)
	if err := killProcessPID(dpid); err != nil {
		t.Fatalf("kill daemon %d: %v", dpid, err)
	}

	waitForDaemonLineCount(t, home, 2, 10*time.Second)

	waitForHolder(t, home, "a")
	observeReattachedHolderFor(t, home, "a", 750*time.Millisecond)
	if err := processPIDAlive(a.cmd.Process.Pid); err != nil {
		t.Fatalf("original holder exited instead of reclaiming its lease: %v", err)
	}
}
