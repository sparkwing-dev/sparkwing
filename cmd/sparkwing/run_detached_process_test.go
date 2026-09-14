package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/testleak"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var (
	submitCLIOnce sync.Once
	submitCLIDir  string
	submitCLIBin  string
	submitCLIErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(submitCLIDir)
	if err := killFixtureSurvivors(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	} else if code == 0 {
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

const fixtureExitGrace = 10 * time.Second

// safety: a supervisor a test leaves behind is reparented to init and keeps
// respawning its worker under a home the suite has already deleted.
func killFixtureSurvivors() error {
	deadline := time.Now().Add(fixtureExitGrace)
	survivors := fixtureProcesses()
	for len(survivors) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		survivors = fixtureProcesses()
	}
	if len(survivors) == 0 {
		return nil
	}
	for _, pid := range survivors {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
	return fmt.Errorf("fixture CLI processes outlived the suite: %v", survivors)
}

func fixtureProcesses() []int {
	if submitCLIDir == "" {
		return nil
	}
	out, err := exec.Command("ps", "-eww", "-o", "pid=,args=").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture survivor check disarmed: ps: %v\n", err)
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, submitCLIDir) {
			continue
		}
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(line), &pid); err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

func buildSubmitCLI(t *testing.T) string {
	t.Helper()
	submitCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sparkwing-submit-cli")
		if err != nil {
			submitCLIErr = err
			return
		}
		submitCLIDir = dir
		bin := filepath.Join(dir, "sparkwing")
		overlay, err := seededBundleOverlay(dir)
		if err != nil {
			submitCLIErr = err
			return
		}
		cmd := exec.Command("go", "build", "-overlay", overlay, "-o", bin, "github.com/sparkwing-dev/sparkwing/cmd/sparkwing")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			submitCLIErr = err
			return
		}
		submitCLIBin = bin
	})
	if submitCLIErr != nil {
		t.Fatalf("build sparkwing CLI: %v", submitCLIErr)
	}
	return submitCLIBin
}

// safety: a reproducible build trims the compiled-in source path, so resolving the
// repository from runtime.Caller yields a module path the filesystem does not hold.
// This resolves at package initialization because tests in this package change the
// working directory, and a later walk would start from wherever one of them left it.
var moduleRootDir, moduleRootErr = func() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}()

func moduleRoot() (string, error) {
	return moduleRootDir, moduleRootErr
}

// safety: the dashboard bundle is a gitignored artifact of bin/build-web.sh, so
// without a stub this binary's dashboard depends on the developer's build state.
func seededBundleOverlay(dir string) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(root, "internal", "web", "next-out")); err != nil {
		return "", fmt.Errorf("locate the embedded dashboard directory: %w", err)
	}
	index := filepath.Join(dir, "index.html")
	if err = os.WriteFile(index, []byte("<!doctype html><title>sparkwing test bundle</title>\n"), 0o600); err != nil {
		return "", fmt.Errorf("write stub dashboard index: %w", err)
	}
	document, err := json.Marshal(map[string]map[string]string{
		"Replace": {filepath.Join(root, "internal", "web", "next-out", "index.html"): index},
	})
	if err != nil {
		return "", fmt.Errorf("encode build overlay: %w", err)
	}
	overlay := filepath.Join(dir, "bundle-overlay.json")
	if err = os.WriteFile(overlay, document, 0o600); err != nil {
		return "", fmt.Errorf("write build overlay: %w", err)
	}
	return overlay, nil
}

const submitFixtureSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"fixture\"}]")
		return
	}
	f, err := os.OpenFile(os.Getenv("SPARKWING_SUBMIT_TEST_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, os.Args[len(os.Args)-1])
	if envMarker := os.Getenv("SPARKWING_SUBMIT_TEST_ENV_MARKER"); envMarker != "" {
		ef, err := os.OpenFile(envMarker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			panic(err)
		}
		defer ef.Close()
		fmt.Fprintln(ef, os.Getenv("SPARKWING_SUBMIT_TEST_ENV"))
	}
}
`

type submitTestEnv struct {
	t         *testing.T
	bin       string
	home      string
	repoDir   string
	marker    string
	envMarker string
	extraEnv  []string
}

func newSubmitTestEnv(t *testing.T) *submitTestEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the detached-consumer contract is exercised on POSIX process semantics")
	}

	home, err := os.MkdirTemp("", "swh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	repoDir := t.TempDir()
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.mod"),
		[]byte("module submitfixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"),
		[]byte(submitFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &submitTestEnv{
		t:         t,
		bin:       buildSubmitCLI(t),
		home:      home,
		repoDir:   repoDir,
		marker:    filepath.Join(repoDir, "dispatched.txt"),
		envMarker: filepath.Join(repoDir, "environment.txt"),
	}
	t.Cleanup(env.stopDaemon)
	t.Cleanup(env.stopConsumer)
	return env
}

func (e *submitTestEnv) env() []string {
	base := append(os.Environ(),
		"SPARKWING_HOME="+e.home,

		"SPARKWING_REPOS="+filepath.Join(e.home, "repos.yaml"),
		"SPARKWING_NO_UPDATE=1",
		"SPARKWING_SUBMIT_TEST_MARKER="+e.marker,
		"SPARKWING_SUBMIT_TEST_ENV_MARKER="+e.envMarker,
	)
	return append(base, e.extraEnv...)
}

func (e *submitTestEnv) run(args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env()
	cmd.Dir = e.repoDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *submitTestEnv) runStdout(args ...string) (string, string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env()
	cmd.Dir = e.repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (e *submitTestEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("sparkwing %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// detachArgs spells one detached launch: every sparkwing flag follows the
// pipeline name, and SPARKWING_HOME in the test environment selects the home.
func (e *submitTestEnv) detachArgs(pipeline string, extra ...string) []string {
	args := []string{"run", pipeline, "--sw-detached", "--sw-cd", e.repoDir}
	return append(args, extra...)
}

func (e *submitTestEnv) submit(extra ...string) submitResult {
	e.t.Helper()
	return e.submitWithArgs(extra, nil)
}

func (e *submitTestEnv) submitWithArgs(own, pipelineArgs []string) submitResult {
	e.t.Helper()
	args := append(e.detachArgs("fixture", "--sw-output", "json"), own...)
	args = append(args, pipelineArgs...)
	out, errOut, err := e.runStdout(args...)
	if err != nil {
		e.t.Fatalf("sparkwing %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, out, errOut)
	}
	var r submitResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		e.t.Fatalf("decode submit ack: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	return r
}

func (e *submitTestEnv) store() *store.Store {
	e.t.Helper()
	st, err := store.Open(orchestrator.PathsAt(e.home).StateDB())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = st.Close() })
	return st
}

func (e *submitTestEnv) markerLines() []string {
	return linesIn(e.marker)
}

func linesIn(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (e *submitTestEnv) stopConsumer() {
	if pid, ok := orchestrator.ConsumerPID(e.home); ok {
		if err := stopSupervisor(pid, ""); err != nil {
			e.t.Errorf("stop consumer process %d: %v", pid, err)
			return
		}
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			if !processAlive(pid) {
				return
			}
			select {
			case <-poll.C:
			case <-deadline.C:
				e.t.Errorf("consumer process %d did not exit within cleanup bound", pid)
				return
			}
		}
	}
}

// safety: the CLI pre-warms a daemon in this home and nothing else ends it,
// so it outlives the test binary.
func (e *submitTestEnv) stopDaemon() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := wingdclient.Stop(ctx, wingdclient.Options{Home: e.home}); err != nil &&
		!errors.Is(err, wingdclient.ErrNoDaemon) {
		e.t.Errorf("stop wingd for home %s: %v", e.home, err)
	}
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
	}
}

func TestRunsConsumer_StatusAndStopReportTheResidentProcess(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	if out, err := e.run("runs", "consumer", "status", "--home", e.home); err == nil {
		t.Fatalf("status exited 0 with no consumer running:\n%s", out)
	}

	e.mustRun("runs", "consumer", "start", "--home", e.home)
	out := e.mustRun("runs", "consumer", "status", "--home", e.home)
	if records := decodeOutputRecords(t, []byte(out)); len(records) != 1 || records[0]["service"] != "consumer" || records[0]["state"] != "running" {
		t.Fatalf("status did not report a running consumer:\n%s", out)
	}

	out = e.mustRun("runs", "consumer", "stop", "--home", e.home)
	if !strings.Contains(out, "stopped") {
		t.Fatalf("stop did not report stopping:\n%s", out)
	}
	waitUntil(t, "the stopped consumer to release the queue", 10*time.Second, func() bool {
		running, err := orchestrator.ConsumerRunning(e.home)
		return err == nil && !running
	})
}

func TestRunsCancel_CancelsAQueuedRunWithoutTouchingItsReplacement(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	ctx := context.Background()
	st := e.store()

	for _, id := range []string{"run-target", "run-replacement"} {
		now := time.Now()
		if err := st.CreateTrigger(ctx, store.Trigger{
			ID: id, Pipeline: "fixture", CreatedAt: now, TriggerSource: "runs-submit",
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateRun(ctx, store.Run{
			ID: id, Pipeline: "fixture", Status: "pending",
			TriggerSource: "runs-submit", CreatedAt: now, StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	out := e.mustRun("runs", "cancel", "--run", "run-target", "--home", e.home)
	if !strings.Contains(out, "cancelled before dispatch") {
		t.Fatalf("cancel did not report cancelling a queued run:\n%s", out)
	}

	target, err := st.GetRun(ctx, "run-target")
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "cancelled" {
		t.Fatalf("target run status = %q, want cancelled", target.Status)
	}
	replacement, err := st.GetRun(ctx, "run-replacement")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Status != "pending" {
		t.Fatalf("cancelling one run changed its replacement to %q", replacement.Status)
	}
}

const blockingFixtureSource = `package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"fixture\"}]")
		return
	}
	marker := os.Getenv("SPARKWING_SUBMIT_TEST_MARKER")
	id := os.Args[len(os.Args)-1]
	appendLine(marker, "START "+id+" pid="+strconv.Itoa(os.Getpid()))
	resp, err := http.Get(os.Getenv("ADV_HOLD_URL"))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	appendLine(marker, "END "+id+" pid="+strconv.Itoa(os.Getpid()))
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}
`

func (e *submitTestEnv) useBlockingFixture(t *testing.T) <-chan struct{} {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", "main.go"),
		[]byte(blockingFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	e.extraEnv = append(e.extraEnv, "ADV_HOLD_URL="+server.URL, "SPARKWING_SUBMIT_ENV_ALLOW=ADV_HOLD_URL")
	return started
}

func waitForFixtureHold(t *testing.T, started <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-started:
	case <-timer.C:
		t.Fatal("fixture did not enter its blocking request within 10s")
	}
}

func (e *submitTestEnv) startsInMarker() int {
	n := 0
	for _, l := range e.markerLines() {
		if strings.HasPrefix(l, "START ") {
			n++
		}
	}
	return n
}

func TestRunDetached_RefAndPriorityAreCarried(t *testing.T) {
	t.Parallel()
	if err := refuseForegroundOnlyFlags(runFlags{ref: "main", priority: "front", prioritySet: true}); err != nil {
		t.Fatalf("--sw-ref/--sw-priority are carried on the trigger but were refused: %v", err)
	}
}

func TestRunDetached_RefusesAForegroundOnlyFlag(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	out, err := e.run(append(e.detachArgs("fixture"), "--sw-dry-run")...)
	if err == nil {
		t.Fatalf("--sw-dry-run was accepted for a detached run:\n%s", out)
	}
	for _, want := range []string{"--sw-dry-run", "foreground"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal missing %q:\n%s", want, out)
		}
	}
	triggers, terr := e.store().ListTriggers(context.Background(), store.TriggerFilter{Limit: 10})
	if terr != nil {
		t.Fatal(terr)
	}
	if len(triggers) != 0 {
		t.Fatalf("a refused launch still queued %d triggers", len(triggers))
	}
}

func TestRun_RefusesADetachedOnlyFlagWithoutDetached(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	for _, flag := range []string{
		"--sw-idempotency-key", "--sw-request-id",
		"--sw-consumer-idle", "--sw-consumer-claim-lease", "--sw-output",
	} {
		out, err := e.run("run", "fixture", "--sw-cd", e.repoDir, flag, "value")
		if err == nil {
			t.Errorf("%s was accepted without --sw-detached:\n%s", flag, out)
			continue
		}
		for _, want := range []string{flag, "--sw-detached"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal for %s missing %q:\n%s", flag, want, out)
			}
		}
	}
}

func TestRunsSubmit_IsNoLongerASubcommand(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	out, err := e.run("runs", "submit", "fixture")
	if err == nil {
		t.Fatalf("`runs submit` still runs:\n%s", out)
	}
	if !strings.Contains(out, "unknown command") {
		t.Fatalf("`runs submit` did not report an unknown subcommand:\n%s", out)
	}
}

func trigArgsHas(t *testing.T, e *submitTestEnv, runID, key string) (string, bool) {
	t.Helper()
	trig, err := e.store().GetTrigger(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := trig.Args[key]
	return v, ok
}

func TestRunDetached_RepeatKeyAgainstADifferentTreeIsRefused(t *testing.T) {
	t.Parallel()
	const first, second = "aaaaaaaaaaaa", "bbbbbbbbbbbb"
	cases := []struct {
		name    string
		stored  string
		next    string
		refused bool
	}{
		{"same commit is a retry", first, first, false},
		{"different commit is a different request", first, second, true},
		{"ref added to a ref-less original", "", second, true},
		{"ref dropped from a ref original", first, "", true},
		{"neither names a ref", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			existing := &store.Trigger{ID: "run-1", Pipeline: "deploy"}
			if tc.stored != "" {
				existing.TriggerEnv = map[string]string{orchestrator.RefWorktreeRevKey: tc.stored}
			}
			err := checkRefMatchesOriginal(existing, submission{IdempotencyKey: "k"}, orchestrator.Commit(tc.next))
			if tc.refused && err == nil {
				t.Fatal("a repeat naming a different tree was answered with the original run")
			}
			if !tc.refused && err != nil {
				t.Fatalf("a genuine retry was refused: %v", err)
			}
			if tc.refused && !strings.Contains(err.Error(), "run-1") {
				t.Errorf("refusal does not name the original run: %v", err)
			}
		})
	}
}
