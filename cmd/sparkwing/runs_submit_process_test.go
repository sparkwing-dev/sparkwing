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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/testleak"
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
	if code == 0 {
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
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
		cmd := exec.Command("go", "build", "-o", bin, "github.com/sparkwing-dev/sparkwing/cmd/sparkwing")
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

func TestRunsSubmit_UsesEachSubmissionEnvironment(t *testing.T) {
	e := newSubmitTestEnv(t)
	e.extraEnv = []string{"SPARKWING_SUBMIT_TEST_ENV=first"}
	e.submit()
	waitUntil(t, "first submitted environment", 90*time.Second, func() bool {
		return len(linesIn(e.envMarker)) == 1
	})

	e.extraEnv = []string{"SPARKWING_SUBMIT_TEST_ENV=second"}
	e.submit()
	waitUntil(t, "second submitted environment", 90*time.Second, func() bool {
		return len(linesIn(e.envMarker)) == 2
	})

	if got := linesIn(e.envMarker); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("execution environments = %q, want one snapshot from each submission", got)
	}
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

func (e *submitTestEnv) submit(extra ...string) submitResult {
	e.t.Helper()
	return e.submitWithArgs(extra, nil)
}

func (e *submitTestEnv) submitWithArgs(own, pipelineArgs []string) submitResult {
	e.t.Helper()
	args := append([]string{"runs", "submit", "-o", "json", "--home", e.home, "-C", e.repoDir}, own...)
	args = append(args, "fixture")
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

func TestRunsSubmit_ExecutionOutlivesTheSubmittingProcess(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	ack := e.submit()
	if ack.RunID == "" {
		t.Fatal("submit acknowledged without a run id")
	}
	if ack.LogPath == "" {
		t.Fatal("submit acknowledged without a log path")
	}
	if info, err := os.Stat(ack.LogPath); err != nil || !info.IsDir() {
		t.Fatalf("acknowledged log_path %q is not a directory: %v", ack.LogPath, err)
	}

	var pid int
	waitUntil(t, "a detached consumer to hold the queue", 10*time.Second, func() bool {
		var ok bool
		pid, ok = orchestrator.ConsumerPID(e.home)
		return ok
	})
	if pid == os.Getpid() {
		t.Fatal("the test process is hosting the consumer; the run is not detached")
	}

	st := e.store()
	if _, err := st.GetTrigger(context.Background(), ack.RunID); err != nil {
		t.Fatalf("acknowledged run has no trigger row: %v", err)
	}
	if _, err := st.GetRun(context.Background(), ack.RunID); err != nil {
		t.Fatalf("acknowledged run has no run row: %v", err)
	}

	waitUntil(t, "the detached consumer to execute the submitted run", 90*time.Second, func() bool {
		lines := e.markerLines()
		return len(lines) == 1 && lines[0] == ack.RunID
	})
}

func TestRunsSubmit_DuplicateKeyReturnsTheOriginalRun(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	first := e.submit("--idempotency-key", "deploy-once")
	if first.AlreadySubmitted {
		t.Fatal("the first submission reported itself as a duplicate")
	}
	second := e.submit("--idempotency-key", "deploy-once")
	if second.RunID != first.RunID {
		t.Fatalf("resubmission produced %q, want the original %q", second.RunID, first.RunID)
	}
	if !second.AlreadySubmitted {
		t.Fatal("resubmission did not report already_submitted")
	}

	triggers, err := e.store().ListTriggers(context.Background(), store.TriggerFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 1 {
		t.Fatalf("two submissions under one key created %d triggers, want 1", len(triggers))
	}
}

func TestRunsSubmit_DistinctKeysAreDistinctRuns(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	a := e.submit("--idempotency-key", "a")
	b := e.submit("--idempotency-key", "b")
	if a.RunID == b.RunID {
		t.Fatalf("distinct keys collapsed onto one run %q", a.RunID)
	}
	if b.AlreadySubmitted {
		t.Fatal("a fresh key was treated as a duplicate")
	}
}

func TestRunsSubmit_RequestIDDoesNotDeduplicate(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	a := e.submit("--request-id", "trace-1")
	b := e.submit("--request-id", "trace-1")
	if a.RunID == b.RunID {
		t.Fatal("a repeated request id deduplicated the submission")
	}
	if b.AlreadySubmitted {
		t.Fatal("a repeated request id was reported as already submitted")
	}
	trig, err := e.store().GetTrigger(context.Background(), a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := trig.TriggerEnv[SubmitRequestIDKey]; got != "trace-1" {
		t.Fatalf("request id recorded as %q, want trace-1", got)
	}
	if trig.IdempotencyKey != "" {
		t.Fatalf("request id leaked into the idempotency key: %q", trig.IdempotencyKey)
	}
}

func TestRunsSubmit_PendingWorkRecoversAfterConsumerRestart(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	first := e.submit()
	waitUntil(t, "the first submitted run to execute", 90*time.Second, func() bool {
		return len(e.markerLines()) == 1
	})
	_ = first

	pid, ok := orchestrator.ConsumerPID(e.home)
	if !ok {
		t.Fatal("no consumer to kill")
	}
	if err := signalKill(pid); err != nil {
		t.Fatalf("kill consumer: %v", err)
	}
	waitUntil(t, "the killed consumer's lock to be released", 10*time.Second, func() bool {
		running, err := orchestrator.ConsumerRunning(e.home)
		return err == nil && !running
	})

	st := e.store()
	ctx := context.Background()
	now := time.Now()
	const recovered = "run-recovered"
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: recovered, Pipeline: "fixture", CreatedAt: now, TriggerSource: "runs-submit",
		TriggerEnv: map[string]string{orchestrator.SubmitRepoDirKey: e.repoDir},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: recovered, Pipeline: "fixture", Status: "pending",
		TriggerSource: "runs-submit", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	e.mustRun("runs", "consumer", "start", "--home", e.home)

	waitUntil(t, "the restarted consumer to execute the queued run", 60*time.Second, func() bool {
		lines := e.markerLines()
		return len(lines) == 2 && lines[1] == recovered
	})
}

func TestRunsConsumer_StatusAndStopReportTheResidentProcess(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	if out, err := e.run("runs", "consumer", "status", "--home", e.home); err == nil {
		t.Fatalf("status exited 0 with no consumer running:\n%s", out)
	}

	e.mustRun("runs", "consumer", "start", "--home", e.home)
	out := e.mustRun("runs", "consumer", "status", "--home", e.home)
	if !strings.Contains(out, "trigger consumer running") {
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

func TestRunsSubmit_RefusesASubmitFlagPlacedAfterThePipelineName(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	for _, flag := range []string{"--idempotency-key", "--sw-ref"} {
		out, err := e.run("runs", "submit", "--home", e.home, "-C", e.repoDir,
			"fixture", flag, "misplaced")
		if err == nil {
			t.Fatalf("a misplaced %s was accepted:\n%s", flag, out)
		}
		for _, want := range []string{flag, "before the pipeline name", "--"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal missing %q:\n%s", want, out)
			}
		}
	}
	out, err := e.run("runs", "submit", "--home", e.home, "-C", e.repoDir,
		"fixture", "--idempotency-key", "misplaced")
	if err == nil {
		t.Fatalf("a misplaced --idempotency-key was accepted:\n%s", out)
	}
	triggers, terr := e.store().ListTriggers(context.Background(), store.TriggerFilter{Limit: 10})
	if terr != nil {
		t.Fatal(terr)
	}
	if len(triggers) != 0 {
		t.Fatalf("a refused submission still queued %d triggers", len(triggers))
	}
}

func TestRunsSubmit_SeparatorHandsAConflictingFlagToThePipeline(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	out := e.mustRun("runs", "submit", "-o", "json", "--home", e.home, "-C", e.repoDir,
		"fixture", "--", "--request-id", "belongs-to-the-pipeline")
	var r submitResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("decode ack: %v\n%s", err, out)
	}
	if r.RequestID != "" {
		t.Fatalf("a pipeline argument after `--` was read as submit's own request id: %q", r.RequestID)
	}
	trig, err := e.store().GetTrigger(context.Background(), r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := trig.Args["request-id"]; got != "belongs-to-the-pipeline" {
		t.Fatalf("pipeline args = %#v, want the flag passed through", trig.Args)
	}
}

func TestRunsSubmit_RefusesAPipelineNothingDeclares(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	out, err := e.run("runs", "submit", "--home", e.home, "-C", e.repoDir, "no-such-pipeline")
	if err == nil {
		t.Fatalf("submitting an unknown pipeline succeeded:\n%s", out)
	}
	if !strings.Contains(out, "no-such-pipeline") {
		t.Fatalf("refusal does not name the pipeline:\n%s", out)
	}
	st := e.store()
	triggers, terr := st.ListTriggers(context.Background(), store.TriggerFilter{Limit: 10})
	if terr != nil && !errors.Is(terr, store.ErrNotFound) {
		t.Fatal(terr)
	}
	if len(triggers) != 0 {
		t.Fatalf("a refused submission still queued %d triggers", len(triggers))
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

func TestRunsSubmit_LiveDispatchSurvivesAWallClockJump(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	holdStarted := e.useBlockingFixture(t)

	ack := e.submit("--consumer-claim-lease", "24s")
	waitUntil(t, "the dispatch to start executing", 120*time.Second, func() bool {
		return e.startsInMarker() >= 1
	})
	waitForFixtureHold(t, holdStarted)

	st := e.store()
	liveBefore, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if liveBefore.Status != "claimed" {
		t.Fatalf("live trigger status before jump = %q, want claimed", liveBefore.Status)
	}
	if liveBefore.LeaseExpiresAt == nil {
		t.Fatal("live trigger has no claim lease")
	}
	initialLease := *liveBefore.LeaseExpiresAt

	probeID := ack.RunID + "-sweep-probe"
	now := time.Now()
	if err := st.CreateRun(context.Background(), store.Run{
		ID: probeID, Pipeline: "fixture", Status: "pending", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(context.Background(), probeID, "success", ""); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, "the live claim heartbeat", 10*time.Second, func() bool {
		live, err := st.GetTrigger(context.Background(), ack.RunID)
		if err != nil {
			t.Fatal(err)
		}
		return live.LeaseExpiresAt != nil && live.LeaseExpiresAt.After(initialLease)
	})
	liveBefore, err = st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE triggers SET lease_expires_at = ?
WHERE id = ? AND status = 'claimed' AND claim_seq = ?`,
		time.Now().Add(-time.Hour).UnixNano(), ack.RunID, liveBefore.ClaimSeq)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("expire live claim rows=%d err=%v, want 1", changed, err)
	}
	if _, err := tx.Exec(`
INSERT INTO triggers (id, pipeline, status, created_at, claimed_at, lease_expires_at, claim_seq)
VALUES (?, ?, 'claimed', ?, ?, ?, 1)`, probeID, "fixture", now.UnixNano(), now.UnixNano(),
		now.Add(-time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if e.startsInMarker() > 1 {
			t.Fatalf("run %s was dispatched %d times concurrently after a wall-clock jump:\n  %s",
				ack.RunID, e.startsInMarker(), strings.Join(e.markerLines(), "\n  "))
		}
		probe, err := st.GetTrigger(context.Background(), probeID)
		if err != nil {
			t.Fatal(err)
		}
		if probe.Status == "done" {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("maintenance sweep did not reconcile the probe within 5s; status = %q", probe.Status)
		}
	}
	liveAfter, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if liveAfter.LeaseExpiresAt == nil || !liveAfter.LeaseExpiresAt.Before(time.Now()) {
		t.Fatalf("live trigger heartbeat renewed before maintenance observed the expired row: lease = %v", liveAfter.LeaseExpiresAt)
	}
	if liveAfter.Status != liveBefore.Status || liveAfter.ClaimSeq != liveBefore.ClaimSeq {
		t.Fatalf("live trigger changed during sweep: status/claim_seq = %s/%d, want %s/%d",
			liveAfter.Status, liveAfter.ClaimSeq, liveBefore.Status, liveBefore.ClaimSeq)
	}
	if got := e.startsInMarker(); got != 1 {
		t.Fatalf("expected exactly one dispatch, saw %d: %v", got, e.markerLines())
	}
}

func TestRunsSubmit_IdempotencyKeyDoesNotCrossPipelines(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	other := t.TempDir()
	t.Cleanup(e.stopConsumer)
	otherSparkwing := filepath.Join(other, ".sparkwing")
	if err := os.MkdirAll(otherSparkwing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherSparkwing, "go.mod"),
		[]byte("module otherfixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherSparkwing, "main.go"),
		[]byte(strings.Replace(submitFixtureSource, `\"name\":\"fixture\"`, `\"name\":\"beta\"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	first := e.submit("--idempotency-key", "shared-key")
	if first.Pipeline != "fixture" {
		t.Fatalf("first submission is pipeline %q", first.Pipeline)
	}

	out, errOut, rerr := e.runStdout("runs", "submit", "-o", "json", "--home", e.home,
		"-C", other, "--idempotency-key", "shared-key", "beta")
	if rerr != nil {
		t.Fatalf("submitting beta failed: %v\nstdout:\n%s\nstderr:\n%s", rerr, out, errOut)
	}
	var second submitResult
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if second.RunID == first.RunID {
		t.Fatalf("submitting beta returned fixture's run %s; beta would never run", first.RunID)
	}
	if second.Pipeline != "beta" {
		t.Fatalf("second submission reported pipeline %q, want beta", second.Pipeline)
	}
	if second.AlreadySubmitted {
		t.Fatal("a key used by a different pipeline was treated as a duplicate")
	}
}

func TestRunsSubmit_DuplicateKeyWithDifferentArgsIsRefused(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	e.submitWithArgs([]string{"--idempotency-key", "k"}, []string{"--env", "staging"})

	out, err := e.run("runs", "submit", "--home", e.home, "-C", e.repoDir,
		"--idempotency-key", "k", "fixture", "--env", "production")
	if err == nil {
		t.Fatalf("a key reused with different arguments was accepted:\n%s", out)
	}
	for _, want := range []string{"different arguments", "staging", "production", "new key"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal missing %q:\n%s", want, out)
		}
	}
}

func TestRunsSubmit_DuplicateAckCarriesTheOriginalStatus(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	first := e.submit("--idempotency-key", "k")

	st := e.store()
	if err := st.FinishRun(context.Background(), first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}

	out, errOut, rerr := e.runStdout("runs", "submit", "-o", "json", "--home", e.home,
		"-C", e.repoDir, "--idempotency-key", "k", "fixture")
	if rerr != nil {
		t.Fatalf("resubmit failed: %v\nstdout:\n%s\nstderr:\n%s", rerr, out, errOut)
	}
	var second submitResult
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if second.Status != "failed" {
		t.Fatalf("duplicate ack status = %q, want failed", second.Status)
	}

	pretty := e.mustRun("runs", "submit", "-o", "pretty", "--home", e.home, "-C", e.repoDir,
		"--idempotency-key", "k", "fixture")
	if !strings.Contains(pretty, "failed") {
		t.Errorf("pretty duplicate ack hides the original's failure:\n%s", pretty)
	}
	if !strings.Contains(pretty, "already finished") {
		t.Errorf("pretty duplicate ack does not say the run is over:\n%s", pretty)
	}
}

func TestRunsSubmit_ReplacesAConsumerFromAnotherBuild(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	old := exec.Command(e.bin, "__runs-consume", "--home", e.home,
		"--idle", "10m", "--version", "v0.0.1-old")
	old.Env = e.env()
	old.Dir = e.repoDir
	logF, _ := os.Create(filepath.Join(e.home, "old-consumer.log"))
	old.Stdout, old.Stderr = logF, logF
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Process.Kill(); _ = old.Wait() })
	waitUntil(t, "the old consumer to take the queue", 20*time.Second, func() bool {
		running, err := orchestrator.ConsumerRunning(e.home)
		return err == nil && running
	})
	oldPID, _ := orchestrator.ConsumerPID(e.home)

	e.submit()

	info, ok := orchestrator.ConsumerInfo(e.home)
	if !ok {
		t.Fatal("no consumer after submitting against an outdated one")
	}
	if info.PID == oldPID {
		t.Fatalf("the outdated consumer (pid %d, v0.0.1-old) still owns the queue; "+
			"an upgrade would never take effect", oldPID)
	}
	if info.Version == "v0.0.1-old" {
		t.Fatalf("replacement consumer still reports the old version %q", info.Version)
	}
	waitUntil(t, "the new consumer to execute the run", 90*time.Second, func() bool {
		return len(e.markerLines()) >= 1
	})
}

func TestRunsConsumerStop_RecordsTheInterruptedRun(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	holdStarted := e.useBlockingFixture(t)

	ack := e.submit()
	waitUntil(t, "the dispatch to start", 120*time.Second, func() bool {
		return e.startsInMarker() >= 1
	})
	waitForFixtureHold(t, holdStarted)

	e.mustRun("runs", "consumer", "stop", "--home", e.home)

	st := e.store()
	waitUntil(t, "the interrupted run's trigger to leave the claimed state", 30*time.Second, func() bool {
		trig, err := st.GetTrigger(context.Background(), ack.RunID)
		return err == nil && trig.Status != "claimed"
	})
	trig, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "claimed" {
		t.Fatal("stopping the consumer left the run's trigger claimed with nothing executing it")
	}

	if trig.Status == "pending" {
		n, cerr := st.CountPendingTriggers(context.Background())
		if cerr != nil || n == 0 {
			t.Fatalf("trigger reads pending but the queue is empty (n=%d err=%v)", n, cerr)
		}
	}
}

func TestRunsSubmit_RefIsNoLongerUndetachable(t *testing.T) {
	t.Parallel()
	if err := refuseUndetachableFlags([]string{"--sw-ref", "main"}); err != nil {
		t.Fatalf("--sw-ref is still refused as undetachable: %v", err)
	}
}

func TestRunsSubmit_RepeatKeyAgainstADifferentTreeIsRefused(t *testing.T) {
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
			err := checkRefMatchesOriginal(existing, submission{IdempotencyKey: "k"}, tc.next)
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
