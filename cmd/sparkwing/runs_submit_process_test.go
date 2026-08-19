package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// These tests run the real CLI as a separate process against an
// isolated home. Nothing smaller can prove the submission contract: the
// whole claim is that execution outlives the submitting process, and a
// claim about processes has to be checked with processes.

var (
	submitCLIOnce sync.Once
	submitCLIBin  string
	submitCLIErr  error
)

// buildSubmitCLI compiles the sparkwing CLI once per test binary.
func buildSubmitCLI(t *testing.T) string {
	t.Helper()
	submitCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sparkwing-submit-cli")
		if err != nil {
			submitCLIErr = err
			return
		}
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

// submitFixtureSource is a stand-in pipeline binary. It answers
// --describe like a real compiled .sparkwing tree and, when dispatched,
// appends the trigger id to a marker file and finishes.
//
// It deliberately has no dependencies. A real .sparkwing module pulls in
// the SDK and takes tens of seconds to build; these tests are about
// process ownership and recovery, and every one of those questions is
// answered by "did the marker line appear, and how many times".
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
}
`

// submitTestEnv is one isolated machine: a home, a fixture checkout, and
// the marker file the fixture writes to.
type submitTestEnv struct {
	t        *testing.T
	bin      string
	home     string
	repoDir  string
	marker   string
	extraEnv []string
}

func newSubmitTestEnv(t *testing.T) *submitTestEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the detached-consumer contract is exercised on POSIX process semantics")
	}
	// A short home keeps every path this test creates well inside the
	// unix-socket length limit that a long macOS TMPDIR would blow.
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
		t:       t,
		bin:     buildSubmitCLI(t),
		home:    home,
		repoDir: repoDir,
		marker:  filepath.Join(repoDir, "dispatched.txt"),
	}
	t.Cleanup(env.stopConsumer)
	return env
}

func (e *submitTestEnv) env() []string {
	base := append(os.Environ(),
		"SPARKWING_HOME="+e.home,
		// The CLI runs as a real process, so paths.UnderTest() is false
		// inside it and the repo registry would otherwise resolve to the
		// developer's own ~/.config/sparkwing/repos.yaml -- letting a test
		// read, and compile, every checkout registered on the machine.
		"SPARKWING_REPOS="+filepath.Join(e.home, "repos.yaml"),
		"SPARKWING_NO_UPDATE=1",
		"SPARKWING_SUBMIT_TEST_MARKER="+e.marker,
	)
	return append(base, e.extraEnv...)
}

// run invokes the CLI and returns its combined output.
func (e *submitTestEnv) run(args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env()
	cmd.Dir = e.repoDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runStdout invokes the CLI and returns stdout only. Diagnostics --
// the consumer-rotation notice, warnings -- go to stderr on purpose, so
// a caller parsing a JSON acknowledgment must not be reading them.
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

// submit runs `runs submit` against the fixture and decodes the ack.
// extra are submit's own flags, which precede the pipeline name.
func (e *submitTestEnv) submit(extra ...string) submitResult {
	e.t.Helper()
	return e.submitWithArgs(extra, nil)
}

// submitWithArgs separates submit's own flags from the pipeline's,
// which is the placement the command requires: everything after the
// pipeline name belongs to the pipeline.
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
	b, err := os.ReadFile(e.marker)
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
			err := syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
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

// TestRunsSubmit_ExecutionOutlivesTheSubmittingProcess is the core
// contract: submit returns, the submitting process is gone, and the run
// still executes. It also pins that the acknowledgment is backed by a
// different process -- an ack from a fork of the submitter would die
// with the terminal, which is the failure this whole feature exists to
// remove.
func TestRunsSubmit_ExecutionOutlivesTheSubmittingProcess(t *testing.T) {
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

	// The submitting process has exited by now -- CombinedOutput waited
	// for it. Whatever owns the run is something else.
	pid, ok := orchestrator.ConsumerPID(e.home)
	if !ok {
		t.Fatal("nothing holds the queue after an acknowledged submission")
	}
	if pid == os.Getpid() {
		t.Fatal("the test process is hosting the consumer; the run is not detached")
	}

	// The run was durable before the ack, so it is visible now regardless
	// of how far execution has got.
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

// TestRunsSubmit_DuplicateKeyReturnsTheOriginalRun is the
// duplicate-submission proof across processes: a caller that resubmits
// after an ambiguous failure must reach the run it already has.
func TestRunsSubmit_DuplicateKeyReturnsTheOriginalRun(t *testing.T) {
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

// TestRunsSubmit_DistinctKeysAreDistinctRuns is the other half of
// dedup. A key scopes one intent; two intents must not collapse.
func TestRunsSubmit_DistinctKeysAreDistinctRuns(t *testing.T) {
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

// TestRunsSubmit_RequestIDDoesNotDeduplicate keeps the two identifiers
// apart. A caller that reuses a tracing id across attempts must still
// get separate runs; folding request_id into dedup would silently
// swallow deliberate resubmissions.
func TestRunsSubmit_RequestIDDoesNotDeduplicate(t *testing.T) {
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

// TestRunsSubmit_PendingWorkRecoversAfterConsumerRestart is the
// recovery contract. A consumer killed outright -- SIGKILL, no chance to
// clean up -- must leave the queue takeable and the work runnable.
func TestRunsSubmit_PendingWorkRecoversAfterConsumerRestart(t *testing.T) {
	e := newSubmitTestEnv(t)

	// Prime the compile cache with one completed submission, so the
	// recovery half is not also measuring a cold Go build.
	first := e.submit()
	waitUntil(t, "the first submitted run to execute", 90*time.Second, func() bool {
		return len(e.markerLines()) == 1
	})
	_ = first

	pid, ok := orchestrator.ConsumerPID(e.home)
	if !ok {
		t.Fatal("no consumer to kill")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill consumer: %v", err)
	}
	waitUntil(t, "the killed consumer's lock to be released", 10*time.Second, func() bool {
		running, err := orchestrator.ConsumerRunning(e.home)
		return err == nil && !running
	})

	// Queue work while nothing is resident, the way a submission racing a
	// consumer's exit would.
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

// TestRunsConsumer_StatusAndStopReportTheResidentProcess covers the
// operator surface, including that a SIGKILLed consumer reads as gone
// with no stale-state cleanup.
func TestRunsConsumer_StatusAndStopReportTheResidentProcess(t *testing.T) {
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

// TestRunsCancel_CancelsAQueuedRunWithoutTouchingItsReplacement is the
// cancellation contract on the path submission adds: a run that no
// consumer has claimed, on a laptop with no dashboard and no profile.
func TestRunsCancel_CancelsAQueuedRunWithoutTouchingItsReplacement(t *testing.T) {
	e := newSubmitTestEnv(t)
	ctx := context.Background()
	st := e.store()

	// Queue two runs with no consumer resident, so neither can be claimed
	// out from under the cancellation mid-test.
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

// TestRunsSubmit_RefusesASubmitFlagPlacedAfterThePipelineName is a
// regression guard on a trap this command had during development.
//
// Parsing stops at the pipeline name, so `runs submit deploy
// --idempotency-key k` handed the key to the pipeline as an argument and
// ran with no deduplication whatsoever -- while printing a perfectly
// normal acknowledgment. A caller cannot detect that from the output,
// which makes silence the worst possible response.
func TestRunsSubmit_RefusesASubmitFlagPlacedAfterThePipelineName(t *testing.T) {
	e := newSubmitTestEnv(t)
	out, err := e.run("runs", "submit", "--home", e.home, "-C", e.repoDir,
		"fixture", "--idempotency-key", "misplaced")
	if err == nil {
		t.Fatalf("a misplaced --idempotency-key was accepted:\n%s", out)
	}
	for _, want := range []string{"--idempotency-key", "before the pipeline name", "--"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal missing %q:\n%s", want, out)
		}
	}
	triggers, terr := e.store().ListTriggers(context.Background(), store.TriggerFilter{Limit: 10})
	if terr != nil {
		t.Fatal(terr)
	}
	if len(triggers) != 0 {
		t.Fatalf("a refused submission still queued %d triggers", len(triggers))
	}
}

// TestRunsSubmit_SeparatorHandsAConflictingFlagToThePipeline is the
// escape hatch: a pipeline that declares its own --request-id must still
// be submittable.
func TestRunsSubmit_SeparatorHandsAConflictingFlagToThePipeline(t *testing.T) {
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

// TestRunsSubmit_RefusesAPipelineNothingDeclares proves the submission
// fails in the caller's terminal rather than landing in the queue and
// failing later where nobody is reading.
func TestRunsSubmit_RefusesAPipelineNothingDeclares(t *testing.T) {
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

// advSlowFixtureSource is a pipeline that records a START/END pair per
// dispatch and can be told to take a long time, so "dispatched twice
// concurrently" is directly observable rather than inferred.
const advSlowFixtureSource = `package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"fixture\"}]")
		return
	}
	marker := os.Getenv("SPARKWING_SUBMIT_TEST_MARKER")
	id := os.Args[len(os.Args)-1]
	appendLine(marker, "START "+id+" pid="+strconv.Itoa(os.Getpid()))
	if s := os.Getenv("ADV_SLEEP_SECONDS"); s != "" {
		n, _ := strconv.Atoi(s)
		time.Sleep(time.Duration(n) * time.Second)
	}
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

// useSlowFixture swaps the environment's checkout for one whose runs
// take sleepSeconds, so a dispatch can be observed while it is alive.
func (e *submitTestEnv) useSlowFixture(t *testing.T, sleepSeconds int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", "main.go"),
		[]byte(advSlowFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}
	e.extraEnv = append(e.extraEnv, "ADV_SLEEP_SECONDS="+strconv.Itoa(sleepSeconds))
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

// TestRunsSubmit_LiveDispatchSurvivesAWallClockJump reproduces a suspended
// laptop resuming with wall time past the claim's lease while
// the heartbeat's next monotonic tick is still far away; the sweep runs
// four times more often than the heartbeat, so it wins. Pushing the
// lease row into the past reproduces exactly the state a resume leaves
// behind. The run must not be dispatched a second time.
func TestRunsSubmit_LiveDispatchSurvivesAWallClockJump(t *testing.T) {
	e := newSubmitTestEnv(t)
	e.useSlowFixture(t, 25)

	ack := e.submit("--consumer-claim-lease", "300s")
	waitUntil(t, "the dispatch to start executing", 120*time.Second, func() bool {
		return e.startsInMarker() >= 1
	})

	// The laptop wakes.
	st := e.store()
	liveBefore, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if liveBefore.Status != "claimed" {
		t.Fatalf("live trigger status before jump = %q, want claimed", liveBefore.Status)
	}
	if _, err := st.DB().Exec(
		`UPDATE triggers SET lease_expires_at = ? WHERE status = 'claimed'`,
		time.Now().Add(-time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}

	// A terminal expired claim is stable evidence that the maintenance
	// sweep ran; unlike elapsed time, it cannot pass while the sweep is
	// wedged or disconnected from the consumer loop.
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
	if _, err := st.DB().Exec(`
INSERT INTO triggers (id, pipeline, status, created_at, claimed_at, lease_expires_at, claim_seq)
VALUES (?, ?, 'claimed', ?, ?, ?, 1)`, probeID, "fixture", now.UnixNano(), now.UnixNano(),
		now.Add(-time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}

	// One full 15-second maintenance interval plus scheduling headroom.
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(20 * time.Second)
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
			t.Fatalf("maintenance sweep did not reconcile the probe within 20s; status = %q", probe.Status)
		}
	}
	liveAfter, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if liveAfter.Status != liveBefore.Status || liveAfter.ClaimSeq != liveBefore.ClaimSeq {
		t.Fatalf("live trigger changed during sweep: status/claim_seq = %s/%d, want %s/%d",
			liveAfter.Status, liveAfter.ClaimSeq, liveBefore.Status, liveBefore.ClaimSeq)
	}
	if got := e.startsInMarker(); got != 1 {
		t.Fatalf("expected exactly one dispatch, saw %d: %v", got, e.markerLines())
	}
}

// TestRunsSubmit_IdempotencyKeyDoesNotCrossPipelines covers a key used by
// one pipeline answering another pipeline's submission with the
// first pipeline's run, at exit 0 -- so the requested pipeline never ran
// and the caller was told everything was fine.
func TestRunsSubmit_IdempotencyKeyDoesNotCrossPipelines(t *testing.T) {
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

// TestRunsSubmit_DuplicateKeyWithDifferentArgsIsRefused pins that a key names
// one intent, so the same key with different arguments is a different request,
// not a retry. Answering it with the original run would silently drop it.
func TestRunsSubmit_DuplicateKeyWithDifferentArgsIsRefused(t *testing.T) {
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

// TestRunsSubmit_DuplicateAckCarriesTheOriginalStatus pins that exit 0 remains
// correct because the run exists, while the caller can see that the run has
// already finished and how.
func TestRunsSubmit_DuplicateAckCarriesTheOriginalStatus(t *testing.T) {
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

// TestRunsSubmit_ReplacesAConsumerFromAnotherBuild covers a consumer keeping
// its queue while work keeps arriving. Without a version check,
// a freshly installed binary would hand every run to the old build --
// including runs submitted to pick up a fix.
func TestRunsSubmit_ReplacesAConsumerFromAnotherBuild(t *testing.T) {
	e := newSubmitTestEnv(t)

	// A resident consumer claiming to be some other build.
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

// TestRunsConsumerStop_RecordsTheInterruptedRun covers terminal bookkeeping
// that used to be written through the same context that `stop` had
// just cancelled, so it never landed: the run stayed pending and its
// trigger stayed claimed until a lease lapsed minutes later.
func TestRunsConsumerStop_RecordsTheInterruptedRun(t *testing.T) {
	e := newSubmitTestEnv(t)
	e.useSlowFixture(t, 60)

	ack := e.submit()
	waitUntil(t, "the dispatch to start", 120*time.Second, func() bool {
		return e.startsInMarker() >= 1
	})

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
	// Requeued rather than failed: the run never got a verdict, so the
	// next consumer re-executes it.
	if trig.Status == "pending" {
		n, cerr := st.CountPendingTriggers(context.Background())
		if cerr != nil || n == 0 {
			t.Fatalf("trigger reads pending but the queue is empty (n=%d err=%v)", n, cerr)
		}
	}
}
