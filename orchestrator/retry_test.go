package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// retryCounter tracks per-node execution counts across two runs so the
// skip-passed assertion can inspect whether a previously-succeeded node
// actually re-ran. Reset in each test via the constructor pattern.
type retryCounter struct {
	mu   sync.Mutex
	runs map[string]int
}

func (c *retryCounter) inc(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.runs[name]
	c.runs[name]++
	return n
}

func (c *retryCounter) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[name]
}

// Global counter for the retry test pipeline. One instance per test
// invocation is enforced by the TestRun_SkipPassed* test resetting it
// before each scenario.
var retryCnt = &retryCounter{runs: map[string]int{}}

type retryOut struct {
	Tag string `json:"tag"`
}

type retryBuild struct {
	sparkwing.Base
	sparkwing.Produces[retryOut]
}

func (j *retryBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	out := sparkwing.Out(w, "run", j.run)
	return out.WorkStep, nil
}

func (retryBuild) run(ctx context.Context) (retryOut, error) {
	retryCnt.inc("build")
	return retryOut{Tag: "v9"}, nil
}

// retryDeploy fails on its first attempt (tracked per-process) and
// succeeds on subsequent ones. Confirms that on retry the upstream
// Ref[retryOut] still resolves to "v9" even though build is skipped.
type retryDeploy struct {
	sparkwing.Base
	Build sparkwing.Ref[retryOut]
}

func (j *retryDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.Step("run", j.run)
	return nil, nil
}

func (j *retryDeploy) run(ctx context.Context) error {
	n := retryCnt.inc("deploy")
	got := j.Build.Get(ctx)
	if got.Tag != "v9" {
		return errors.New("build ref did not resolve to v9")
	}
	if n == 0 {
		return errors.New("first-attempt failure")
	}
	return nil
}

type retryPipe struct{ sparkwing.Base }

func (retryPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &retryBuild{})
	sparkwing.Job(plan, "deploy", &retryDeploy{Build: sparkwing.RefTo[retryOut](build)}).Needs(build)
	return nil
}

func init() {
	register("retry-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &retryPipe{} })
}

// TestRun_SkipPassedOnRetry verifies the skip-passed rehydration path:
// a first run fails at deploy after build succeeds; a retry with
// RetryOf set should skip build (not re-execute it) and re-run deploy,
// which now succeeds because the per-process counter has advanced. The
// critical assertion is that build's Ref still resolves correctly on
// the retry even though build never ran in that run's process.
func TestRun_SkipPassedOnRetry(t *testing.T) {
	retryCnt = &retryCounter{runs: map[string]int{}}
	p := newPaths(t)

	first, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "retry-pipe", RunID: "first"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "failed" {
		t.Fatalf("first run status = %q, want failed", first.Status)
	}
	if retryCnt.get("build") != 1 {
		t.Fatalf("after first run, build ran %d times, want 1", retryCnt.get("build"))
	}
	if retryCnt.get("deploy") != 1 {
		t.Fatalf("after first run, deploy ran %d times, want 1", retryCnt.get("deploy"))
	}

	second, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "retry-pipe", RunID: "second", RetryOf: first.RunID})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Status != "success" {
		t.Fatalf("second run status = %q, want success (err=%v)", second.Status, second.Error)
	}
	// build was passed in the first run, so skip-passed should NOT
	// invoke it again on the retry. The counter must still read 1.
	if got := retryCnt.get("build"); got != 1 {
		t.Fatalf("build counter = %d after retry, want 1 (build should not re-run)", got)
	}
	// deploy failed in the first run, so it re-runs on retry.
	if got := retryCnt.get("deploy"); got != 2 {
		t.Fatalf("deploy counter = %d after retry, want 2", got)
	}

	// Store-side assertions: the retry's build node row should be
	// marked done with the rehydrated output, and a node_skipped_from_retry
	// event should be recorded.
	st, _ := store.Open(p.StateDB())
	defer st.Close()
	buildNode, err := st.GetNode(context.Background(), second.RunID, "build")
	if err != nil {
		t.Fatalf("GetNode(second, build): %v", err)
	}
	if buildNode.Outcome != "success" {
		t.Fatalf("build outcome after retry = %q, want success", buildNode.Outcome)
	}
	if len(buildNode.Output) == 0 {
		t.Fatal("build output not rehydrated into retry run's row")
	}
	events, err := st.ListEventsAfter(context.Background(), second.RunID, 0, 1000)
	if err != nil {
		t.Fatalf("ListEventsAfter(second): %v", err)
	}
	var sawSkipEvent bool
	for _, ev := range events {
		if ev.NodeID == "build" && ev.Kind == "node_skipped_from_retry" {
			sawSkipEvent = true
			break
		}
	}
	if !sawSkipEvent {
		t.Fatal("expected node_skipped_from_retry event on rehydrated build node")
	}
}

// TestRun_FullRetryReexecutesAll verifies the Options.Full escape hatch:
// with RetryOf set AND Full=true, every node re-runs regardless of
// prior outcome. build runs again on top of its prior pass.
func TestRun_FullRetryReexecutesAll(t *testing.T) {
	retryCnt = &retryCounter{runs: map[string]int{}}
	p := newPaths(t)

	first, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "retry-pipe", RunID: "first"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "failed" {
		t.Fatalf("first run status = %q, want failed", first.Status)
	}

	second, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "retry-pipe", RunID: "second", RetryOf: first.RunID, Full: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Status != "success" {
		t.Fatalf("second run status = %q (err=%v)", second.Status, second.Error)
	}
	if got := retryCnt.get("build"); got != 2 {
		t.Fatalf("build counter = %d under --full retry, want 2", got)
	}
	if got := retryCnt.get("deploy"); got != 2 {
		t.Fatalf("deploy counter = %d under --full retry, want 2", got)
	}
}
