package wingd_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/testmanager"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testleak"
)

var (
	fixtureOnce sync.Once
	fixtureDir  string
	fixtureBin  string
	fixtureErr  error
)

func TestMain(m *testing.M) {
	os.Exit(testmanager.Run(func() int {
		code := m.Run()
		os.RemoveAll(fixtureDir)
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
		return code
	}))
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
			"github.com/sparkwing-dev/sparkwing/internal/wingd/testdata/testprog")
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
	ctx, cancel := context.WithTimeout(ph.t.Context(), timeout)
	defer cancel()
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatal("process exited before reporting OK")
			}
			if tok, found := strings.CutPrefix(line, "OK "); found {
				return tok
			}
		case <-ctx.Done():
			ph.t.Fatal("timed out waiting for OK")
		}
	}
}

func (ph *procHandle) waitLine(prefix string, timeout time.Duration) string {
	ph.t.Helper()
	ctx, cancel := context.WithTimeout(ph.t.Context(), timeout)
	defer cancel()
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatalf("process exited before printing %q", prefix)
			}
			if rest, found := strings.CutPrefix(line, prefix); found {
				return strings.TrimSpace(rest)
			}
		case <-ctx.Done():
			ph.t.Fatalf("timed out waiting for %q", prefix)
		}
	}
}

func (ph *procHandle) waitQueued(within time.Duration) {
	ph.t.Helper()
	ctx, cancel := context.WithTimeout(ph.t.Context(), within)
	defer cancel()
	for {
		select {
		case line, ok := <-ph.lines:
			if !ok {
				ph.t.Fatal("process exited while expected to remain queued")
			}
			if strings.HasPrefix(line, "OK ") {
				ph.t.Fatalf("process was admitted before queueing: %q", line)
			}
			if strings.HasPrefix(line, "QUEUED ") {
				return
			}
		case <-ctx.Done():
			ph.t.Fatal("process did not report its queued state")
		}
	}
}

func (ph *procHandle) kill() {
	_ = ph.cmd.Process.Kill()
}

func daemonLineCount(t *testing.T, home string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "wingd", "daemons.log"))
	if err != nil {
		t.Fatalf("read daemons.log: %v", err)
	}
	return len(strings.Fields(strings.TrimSpace(string(data))))
}

func stopProcessDaemonOnCleanup(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Stop(ctx, client.Options{Home: home}); err != nil && !errors.Is(err, client.ErrNoDaemon) {
			t.Errorf("stop fixture daemon: %v", err)
		}
	})
}

func TestProcess_ElectionRaceSingleDaemon(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	stopProcessDaemonOnCleanup(t, home)
	const n = 6
	holders := make([]*procHandle, n)
	for i := range holders {
		holders[i] = startProc(t, "hold",
			"--home", home,
			"--run", "r"+strconv.Itoa(i),
			"--sem", "election-"+strconv.Itoa(i),
			"--semaphores-only",
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
	stopProcessDaemonOnCleanup(t, home)
	a := startProc(t, "hold", "--home", home, "--run", "a", "--sem", "lock")
	a.waitOK(10 * time.Second)

	b := startProc(t, "hold", "--home", home, "--run", "b", "--sem", "lock")
	b.waitQueued(5 * time.Second)

	a.kill()
	b.waitOK(5 * time.Second)
}

func TestProcess_CancelOthersEvictsHolderAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	stopProcessDaemonOnCleanup(t, home)
	victim := startProc(t, "hold", "--home", home, "--run", "victim",
		"--sem", "lock", "--sem-policy", "cancel_others", "--semaphores-only")
	victim.waitOK(10 * time.Second)

	aggressor := startProc(t, "hold", "--home", home, "--run", "aggressor",
		"--sem", "lock", "--sem-policy", "cancel_others", "--semaphores-only")
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
	if runtime.GOOS == "linux" {
		testmanager.Scope(t)
		t.Setenv("INVOCATION_ID", "fixture-service")
	}
	home := shortHome(t)
	stopProcessDaemonOnCleanup(t, home)
	h := startProc(t, "hold", "--home", home, "--run", "r", "--cores", "0.1", "--real-spawn")
	h.waitOK(10 * time.Second)

	logPath := filepath.Join(home, "wingd", "d.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "wingd:") || !strings.Contains(string(data), "elected") {
		t.Fatalf("daemon log lacks a meaningful history line:\n%s", data)
	}
}

func TestProcess_PipelineClientStartsDaemonViaHostBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("process test skipped in -short")
	}
	home := shortHome(t)
	stopProcessDaemonOnCleanup(t, home)
	hostBin := buildFixture(t)
	env := []string{"PATH=" + t.TempDir()}
	h := startProcEnv(t, env, "hold",
		"--home", home,
		"--run", "hosted",
		"--cores", "0.1",
		"--external-host", "--host-bin", hostBin,
		"--version", "v1.0.0",
	)
	if got := h.waitLine("DAEMON ", 20*time.Second); got != "v1.0.0" {
		t.Fatalf("daemon version = %q, want v1.0.0 -- the daemon is not the explicitly selected binary", got)
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
	stopProcessDaemonOnCleanup(t, home)
	env := []string{"PATH=" + t.TempDir()}
	h := startProcEnv(t, env, "hold",
		"--home", home, "--run", "hostless", "--cores", "0.1", "--external-host")
	got := h.waitLine("ENSURE-ERR ", 20*time.Second)
	if !strings.Contains(got, "no sparkwing binary is available to host one") {
		t.Fatalf("error = %q, want the no-host sentinel", got)
	}
	if _, err := os.Stat(filepath.Join(home, "wingd", "d.log")); err == nil {
		t.Fatal("a client with no host binary started a daemon anyway")
	}
}
