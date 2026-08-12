package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	t       *testing.T
	bin     string
	home    string
	repoDir string
	marker  string
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
	return append(os.Environ(),
		"SPARKWING_HOME="+e.home,
		// The CLI runs as a real process, so paths.UnderTest() is false
		// inside it and the repo registry would otherwise resolve to the
		// developer's own ~/.config/sparkwing/repos.yaml -- letting a test
		// read, and compile, every checkout registered on the machine.
		"SPARKWING_REPOS="+filepath.Join(e.home, "repos.yaml"),
		"SPARKWING_NO_UPDATE=1",
		"SPARKWING_SUBMIT_TEST_MARKER="+e.marker,
	)
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

func (e *submitTestEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("sparkwing %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// submit runs `runs submit` against the fixture and decodes the ack.
func (e *submitTestEnv) submit(extra ...string) submitResult {
	e.t.Helper()
	args := append([]string{"runs", "submit", "-o", "json", "--home", e.home, "-C", e.repoDir}, extra...)
	args = append(args, "fixture")
	out := e.mustRun(args...)
	var r submitResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		e.t.Fatalf("decode submit ack: %v\n%s", err, out)
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
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
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

	waitUntil(t, "the submitted run to execute exactly once", 90*time.Second, func() bool {
		return len(e.markerLines()) >= 1
	})
	// Give a second dispatch every chance to appear before declaring the
	// count final.
	time.Sleep(2 * time.Second)
	if lines := e.markerLines(); len(lines) != 1 {
		t.Fatalf("deduplicated submission executed %d times: %v", len(lines), lines)
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
