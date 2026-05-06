package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// captureBackend is a StateBackend that records each WriteNodeDispatch
// call without persisting it. Anything else returns nil/zero so nodes
// run to completion through the orchestrator without a real store.
type captureBackend struct {
	captured []store.NodeDispatch
	writeErr error
	gitSHA   string
}

func (b *captureBackend) CreateRun(ctx context.Context, r store.Run) error    { return nil }
func (b *captureBackend) FinishRun(ctx context.Context, _, _, _ string) error { return nil }
func (b *captureBackend) UpdatePlanSnapshot(ctx context.Context, _ string, _ []byte) error {
	return nil
}
func (b *captureBackend) CreateNode(ctx context.Context, _ store.Node) error { return nil }
func (b *captureBackend) StartNode(ctx context.Context, _, _ string) error   { return nil }
func (b *captureBackend) FinishNode(ctx context.Context, _, _, _, _ string, _ []byte) error {
	return nil
}
func (b *captureBackend) FinishNodeWithReason(ctx context.Context, _, _, _, _ string, _ []byte, _ string, _ *int) error {
	return nil
}
func (b *captureBackend) UpdateNodeDeps(ctx context.Context, _, _ string, _ []string) error {
	return nil
}
func (b *captureBackend) UpdateNodeActivity(ctx context.Context, _, _, _ string) error { return nil }
func (b *captureBackend) TouchNodeHeartbeat(ctx context.Context, _, _ string) error    { return nil }
func (b *captureBackend) AppendEvent(ctx context.Context, _, _, _ string, _ []byte) error {
	return nil
}
func (b *captureBackend) AddNodeMetricSample(ctx context.Context, _, _ string, _ store.MetricSample) error {
	return nil
}
func (b *captureBackend) GetLatestRun(ctx context.Context, _ string, _ []string, _ time.Duration) (*store.Run, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) GetNodeOutput(ctx context.Context, _, _ string) ([]byte, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) GetNode(ctx context.Context, _, _ string) (*store.Node, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) GetRun(ctx context.Context, _ string) (*store.Run, error) {
	return &store.Run{GitSHA: b.gitSHA}, nil
}
func (b *captureBackend) EnqueueTrigger(ctx context.Context, _ string, _ map[string]string, _, _, _, _, _, _, _ string) (string, error) {
	return "", nil
}
func (b *captureBackend) FindSpawnedChildTriggerID(ctx context.Context, _, _, _ string) (string, error) {
	return "", nil
}
func (b *captureBackend) CreateDebugPause(ctx context.Context, _ store.DebugPause) error { return nil }
func (b *captureBackend) GetActiveDebugPause(ctx context.Context, _, _ string) (*store.DebugPause, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) ReleaseDebugPause(ctx context.Context, _, _, _, _ string) error { return nil }
func (b *captureBackend) ListDebugPauses(ctx context.Context, _ string) ([]*store.DebugPause, error) {
	return nil, nil
}
func (b *captureBackend) SetNodeStatus(ctx context.Context, _, _, _ string) error { return nil }
func (b *captureBackend) CreateApproval(ctx context.Context, _ store.Approval) error {
	return nil
}
func (b *captureBackend) GetApproval(ctx context.Context, _, _ string) (*store.Approval, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) ResolveApproval(ctx context.Context, _, _, _, _, _ string) (*store.Approval, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	return nil, nil
}
func (b *captureBackend) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	b.captured = append(b.captured, d)
	return nil
}
func (b *captureBackend) GetNodeDispatch(ctx context.Context, _, _ string, _ int) (*store.NodeDispatch, error) {
	return nil, store.ErrNotFound
}
func (b *captureBackend) ListNodeDispatches(ctx context.Context, _, _ string) ([]*store.NodeDispatch, error) {
	return nil, nil
}

// stubJob is a job whose Run does nothing observable. The dispatch
// snapshot doesn't depend on Run side effects, only on the resolved
// input struct, so this minimal implementation is enough to exercise
// the snapshot path.
type stubJob struct {
	Region string `json:"region"`
	Token  string `json:"token,omitempty"`
}

func (j *stubJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.Step("run", func(ctx context.Context) error { return nil })
	return nil, nil
}

// buildNode returns a *sparkwing.Node carrying the given job, with
// any modifiers callers want to set up. Lives here so tests don't
// have to spin a full Plan when only one node is needed.
func buildNode(t *testing.T, id string, job sparkwing.Workable) *sparkwing.Node {
	t.Helper()
	return sparkwing.Job(sparkwing.NewPlan(), id, job)
}

// TestDispatchSnapshot_CapturesEnvelope runs writeDispatchSnapshot
// directly (sidestepping the full executeNode goroutine setup) and
// asserts the captured envelope shape: version, type_name, scalar
// fields round-tripped via JSON.
func TestDispatchSnapshot_CapturesEnvelope(t *testing.T) {
	be := &captureBackend{gitSHA: "deadbeef"}
	r := NewInProcessRunner(Backends{State: be})
	node := buildNode(t, "deploy", &stubJob{Region: "us-east-1"})

	if err := r.writeDispatchSnapshot(context.Background(), "run-1", node); err != nil {
		t.Fatalf("writeDispatchSnapshot: %v", err)
	}
	if len(be.captured) != 1 {
		t.Fatalf("captures: got %d, want 1", len(be.captured))
	}
	d := be.captured[0]
	if d.RunID != "run-1" || d.NodeID != "deploy" {
		t.Fatalf("identity: %+v", d)
	}
	if d.CodeVersion != "deadbeef" {
		t.Fatalf("CodeVersion: %q, want deadbeef", d.CodeVersion)
	}
	if d.Seq != -1 {
		t.Fatalf("expected Seq=-1 (auto-assign), got %d", d.Seq)
	}

	var env dispatchEnvelope
	if err := json.Unmarshal(d.InputEnvelope, &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env.Version != dispatchEnvelopeVersion {
		t.Fatalf("envelope.Version: %d", env.Version)
	}
	if !strings.Contains(env.TypeName, "stubJob") {
		t.Fatalf("type_name: %q", env.TypeName)
	}
	var scalar stubJob
	if err := json.Unmarshal(env.ScalarFields, &scalar); err != nil {
		t.Fatalf("scalar: %v", err)
	}
	if scalar.Region != "us-east-1" {
		t.Fatalf("scalar.Region: %q", scalar.Region)
	}
}

// TestDispatchSnapshot_MaskerRedactsScalar confirms that values
// registered with the run's masker are *** in the persisted scalar
// fields. Belt-and-suspenders for raw os.Getenv-baked tokens that
// bypass sparkwing.Secret.
func TestDispatchSnapshot_MaskerRedactsScalar(t *testing.T) {
	be := &captureBackend{}
	r := NewInProcessRunner(Backends{State: be})
	node := buildNode(t, "deploy", &stubJob{Region: "us-east-1", Token: "supersecret"})

	m := secrets.NewMasker()
	m.Register("supersecret")
	ctx := secrets.WithMasker(context.Background(), m)

	if err := r.writeDispatchSnapshot(ctx, "run-1", node); err != nil {
		t.Fatalf("writeDispatchSnapshot: %v", err)
	}
	d := be.captured[0]
	if strings.Contains(string(d.InputEnvelope), "supersecret") {
		t.Fatalf("envelope leaks plaintext: %s", string(d.InputEnvelope))
	}
	if !strings.Contains(string(d.InputEnvelope), "***") {
		t.Fatalf("expected mask token in envelope, got: %s", string(d.InputEnvelope))
	}
	if d.SecretRedactions < 1 {
		t.Fatalf("secret_redactions = %d, want >=1", d.SecretRedactions)
	}
}

// TestDispatchSnapshot_BestEffortFailureNonFatal — a backend whose
// WriteNodeDispatch errors must surface the error to the caller (so
// the inprocess_runner caller can log it) but the caller decides
// whether to fail the node. This test asserts the error returns and
// no row is captured.
func TestDispatchSnapshot_BestEffortFailureNonFatal(t *testing.T) {
	be := &captureBackend{writeErr: errors.New("backend kaput")}
	r := NewInProcessRunner(Backends{State: be})
	node := buildNode(t, "deploy", &stubJob{})

	err := r.writeDispatchSnapshot(context.Background(), "run-1", node)
	if err == nil || !strings.Contains(err.Error(), "backend kaput") {
		t.Fatalf("expected wrapped backend error, got %v", err)
	}
	if len(be.captured) != 0 {
		t.Fatalf("no rows should have been captured, got %d", len(be.captured))
	}
}

// TestCollectDispatchEnv filters to allowlisted prefixes and overlays
// node EnvMap on top of inherited values. The runID + run row layer
// synthesizes keys that wouldn't be in os.Environ() under laptop
// dispatch (the orchestrator process started without them).
func TestCollectDispatchEnv(t *testing.T) {
	t.Setenv("SPARKWING_FOO", "from-env")
	t.Setenv("GITHUB_TOKEN", "redact-me-in-mask-not-here")
	t.Setenv("UNRELATED_SECRET", "should-not-leak")

	node := buildNode(t, "deploy", &stubJob{}).Env("CUSTOM", "node-value")
	got := collectDispatchEnv(node, "run-7", &store.Run{
		GitBranch: "main", GitSHA: "abc",
		TriggerSource: "webhook",
		GithubOwner:   "me", GithubRepo: "proj",
	})

	if got["SPARKWING_FOO"] != "from-env" {
		t.Fatalf("os.Environ pass-through dropped: %v", got)
	}
	if _, leaked := got["UNRELATED_SECRET"]; leaked {
		t.Fatalf("operator env leaked: %v", got)
	}
	if got["SPARKWING_RUN_ID"] != "run-7" {
		t.Fatalf("synthesized run id missing: %v", got)
	}
	if got["SPARKWING_NODE_ID"] != "deploy" {
		t.Fatalf("synthesized node id missing: %v", got)
	}
	if got["SPARKWING_BRANCH"] != "main" || got["SPARKWING_COMMIT"] != "abc" {
		t.Fatalf("git layer missing: %v", got)
	}
	if got["SPARKWING_TRIGGER_SOURCE"] != "webhook" {
		t.Fatalf("trigger source missing: %v", got)
	}
	if got["GITHUB_REPOSITORY"] != "me/proj" {
		t.Fatalf("github repo missing: %v", got)
	}
	if got["CUSTOM"] != "node-value" {
		t.Fatalf("node EnvMap not overlaid: %v", got)
	}
}

// TestCollectDispatchEnv_NilRun degrades gracefully when the GetRun
// fetch failed or the row is missing.
func TestCollectDispatchEnv_NilRun(t *testing.T) {
	node := buildNode(t, "deploy", &stubJob{})
	got := collectDispatchEnv(node, "run-7", nil)

	if got["SPARKWING_RUN_ID"] != "run-7" {
		t.Fatalf("run id should still synthesize without run row: %v", got)
	}
	if got["SPARKWING_NODE_ID"] != "deploy" {
		t.Fatalf("node id should still synthesize: %v", got)
	}
	if _, ok := got["SPARKWING_BRANCH"]; ok {
		t.Fatalf("branch should be absent without run row: %v", got)
	}
}
