package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var s3IntegRegisterOnce sync.Once

// invocations counter is captured at registration time so each Run
// can verify whether its cacheable step actually executed.
var s3CachedInvocations atomic.Int32

type s3CachedJobOut struct {
	Tag string `json:"tag"`
}

type s3CachedJob struct {
	sparkwing.Base
	sparkwing.Produces[s3CachedJobOut]
}

func (j *s3CachedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (s3CachedJob) run(ctx context.Context) (s3CachedJobOut, error) {
	s3CachedInvocations.Add(1)
	return s3CachedJobOut{Tag: "s3-cached-v1"}, nil
}

type s3CachedPipe struct{ sparkwing.Base }

func (s3CachedPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	node := sparkwing.Job(plan, "build", &s3CachedJob{})
	node.Cache(func(ctx context.Context) sparkwing.CacheKey { return sparkwing.Key("s3-integ", "static-v1") })
	return nil
}

// s3TriggerPipe calls RunAndAwait to force the orchestrator to call
// EnqueueTrigger on the configured state backend. In Mode 2 that
// surfaces s3state.ErrNotSupported wrapped in the run's final error.
type s3TriggerPipe struct{ sparkwing.Base }

func (s3TriggerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "trigger", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[s3CachedJobOut, sparkwing.NoInputs](
			ctx, "s3-integ-cache", "build",
			sparkwing.WithFreshTimeout(5*time.Second),
		)
		return err
	})
	return nil
}

func registerS3IntegPipelines(t *testing.T) {
	t.Helper()
	s3IntegRegisterOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("s3-integ-cache",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &s3CachedPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("s3-integ-trigger",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &s3TriggerPipe{} })
	})
}

// TestS3Sharing_TwoRunsBothSucceed pins down the Mode 2 contract: two
// runners hitting the same cache key against a shared bucket coordinate
// through the conditional-write CAS semaphore, so the second run reuses
// the first's memoized result instead of recomputing. The first run
// computes and writes the cache memo under If-Match; the second run's
// memo acquire sees the entry and replays it. Both runs succeed and the
// cacheable step executes exactly once. This is the cross-runner cache
// reservation BW-322 adds; the no-op fallback (no CAS) is exercised
// separately in TestS3Concurrency_FallsBackWhenPreconditionsIgnored.
func TestS3Sharing_TwoRunsBothSucceed(t *testing.T) {
	registerS3IntegPipelines(t)
	art, logs := openIntegrationS3(t)
	paths := newPaths(t)

	s3CachedInvocations.Store(0)

	for _, label := range []string{"A", "B"} {
		state := s3state.New(art, s3state.WithFlushInterval(20*time.Millisecond))
		res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
			Pipeline:      "s3-integ-cache",
			State:         state,
			LogStore:      logs,
			ArtifactStore: art,
		})
		if err != nil {
			t.Fatalf("Run %s: %v", label, err)
		}
		if res.Status != "success" {
			t.Fatalf("Run %s status = %q (err=%v)", label, res.Status, res.Error)
		}
	}
	if got := s3CachedInvocations.Load(); got != 1 {
		t.Errorf("invocations across two Mode 2 runs = %d, want 1 "+
			"(second run reuses the cross-runner cache memo via CAS)", got)
	}
}

// TestS3Sharing_StateVisibleToDashboard pairs a Run A invocation with
// a separate S3Backend pointed at the same bucket. ListRuns / GetRun /
// ListNodes must return Run A's writes; this is what makes the
// dashboard work in Mode 2.
func TestS3Sharing_StateVisibleToDashboard(t *testing.T) {
	registerS3IntegPipelines(t)
	art, logs := openIntegrationS3(t)
	paths := newPaths(t)

	state := s3state.New(art, s3state.WithFlushInterval(20*time.Millisecond))
	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "s3-integ-cache",
		State:         state,
		LogStore:      logs,
		ArtifactStore: art,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}

	b := backend.NewS3Backend(art, logs)
	got, err := b.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != res.RunID {
		t.Fatalf("GetRun returned %+v, want id %q", got, res.RunID)
	}
	if got.Pipeline != "s3-integ-cache" {
		t.Errorf("pipeline = %q, want s3-integ-cache", got.Pipeline)
	}

	nodes, err := b.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("ListNodes returned no nodes")
	}

	runs, err := b.ListRuns(context.Background(), store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	found := false
	for _, r := range runs {
		if r.ID == res.RunID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListRuns did not surface run %s", res.RunID)
	}
}

// TestS3Sharing_TriggerSurfacesErrNotSupported pins down the Mode 2
// boundary: a pipeline that tries to spawn a child run via
// RunAndAwait fails the run, with the failed node's error message
// describing that triggers aren't supported in S3-only mode. The
// orchestrator wraps individual node errors so the top-level
// Result.Error is "nodes failed: [trigger]"; the precise sentinel
// lives on the failed node's error column, readable via the same
// S3Backend the dashboard uses.
func TestS3Sharing_TriggerSurfacesErrNotSupported(t *testing.T) {
	registerS3IntegPipelines(t)
	art, logs := openIntegrationS3(t)
	paths := newPaths(t)

	state := s3state.New(art, s3state.WithFlushInterval(20*time.Millisecond))
	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "s3-integ-trigger",
		State:         state,
		LogStore:      logs,
		ArtifactStore: art,
	})
	if err != nil {
		if errors.Is(err, s3state.ErrNotSupported) || containsNotSupported(err) {
			return
		}
		t.Fatalf("err = %v, want s3state.ErrNotSupported-shaped failure", err)
	}
	if res.Status == "success" {
		t.Fatalf("trigger pipeline succeeded; expected failure")
	}

	b := backend.NewS3Backend(art, logs)
	nodes, err := b.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var triggerNode *store.Node
	for _, n := range nodes {
		if n.NodeID == "trigger" {
			triggerNode = n
			break
		}
	}
	if triggerNode == nil {
		t.Fatalf("no trigger node in nodes=%+v", nodes)
	}
	if triggerNode.Outcome == "" || triggerNode.Outcome == "success" {
		t.Errorf("trigger node outcome = %q, want a failure", triggerNode.Outcome)
	}
	if !strings.Contains(strings.ToLower(triggerNode.Error), "not supported") &&
		!strings.Contains(triggerNode.Error, "triggers require") {
		t.Errorf("trigger node error = %q, want mention of unsupported / triggers", triggerNode.Error)
	}
}

func containsNotSupported(e error) bool {
	if e == nil {
		return false
	}
	return strings.Contains(strings.ToLower(e.Error()), "not supported")
}
