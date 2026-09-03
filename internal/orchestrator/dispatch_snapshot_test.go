package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type captureBackend struct {
	RunCoordination

	captured []store.NodeDispatch
	writeErr error
	gitSHA   string
}

func (b *captureBackend) Close() error                                        { return nil }
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

func (b *captureBackend) AppendNodeAnnotation(ctx context.Context, _, _, _ string) error { return nil }

func (b *captureBackend) SetNodeSummary(ctx context.Context, _, _, _ string) error { return nil }

func (b *captureBackend) SetStepSummary(ctx context.Context, _, _, _, _ string) error { return nil }

func (b *captureBackend) StartNodeStep(ctx context.Context, _, _, _ string) error { return nil }

func (b *captureBackend) FinishNodeStep(ctx context.Context, _, _, _, _ string) error { return nil }

func (b *captureBackend) SkipNodeStep(ctx context.Context, _, _, _ string) error { return nil }

func (b *captureBackend) AppendStepAnnotation(ctx context.Context, _, _, _, _ string) error {
	return nil
}

func (b *captureBackend) ListNodeSteps(ctx context.Context, _ string) ([]*store.NodeStep, error) {
	return nil, nil
}
func (b *captureBackend) TouchNodeHeartbeat(ctx context.Context, _, _ string) error { return nil }
func (b *captureBackend) TouchRunHeartbeat(ctx context.Context, _ string) error     { return nil }
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

func (b *captureBackend) SetNodeArtifactManifest(ctx context.Context, _, _, _ string) error {
	return nil
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

type stubJob struct {
	Region string `json:"region"`
	Token  string `json:"token,omitempty"`
}

func (j *stubJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(ctx context.Context) error { return nil })
	return nil, nil
}

func buildNode(t *testing.T, id string, job sparkwing.Workable) *sparkwing.JobNode {
	t.Helper()
	return sparkwing.Job(sparkwing.NewPlan(), id, job)
}

func TestDispatchSnapshot_CapturesEnvelope(t *testing.T) {
	be := &captureBackend{gitSHA: "deadbeef"}
	r := NewNodeExecutor(Backends{State: be})
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

func TestDispatchSnapshot_MaskerRedactsScalar(t *testing.T) {
	be := &captureBackend{}
	r := NewNodeExecutor(Backends{State: be})
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

func TestDispatchSnapshot_BestEffortFailureNonFatal(t *testing.T) {
	be := &captureBackend{writeErr: errors.New("backend kaput")}
	r := NewNodeExecutor(Backends{State: be})
	node := buildNode(t, "deploy", &stubJob{})

	err := r.writeDispatchSnapshot(context.Background(), "run-1", node)
	if err == nil || !strings.Contains(err.Error(), "backend kaput") {
		t.Fatalf("expected wrapped backend error, got %v", err)
	}
	if len(be.captured) != 0 {
		t.Fatalf("no rows should have been captured, got %d", len(be.captured))
	}
}

func TestCollectDispatchEnv(t *testing.T) {
	t.Setenv("SPARKWING_FOO", "from-env")
	t.Setenv("UNRELATED_SECRET", "should-not-leak")

	node := buildNode(t, "deploy", &stubJob{}).Env("CUSTOM", "node-value")
	got := collectDispatchEnv(context.Background(), node, "run-7", &store.Run{
		GitBranch: "main", GitSHA: "abc",
		TriggerSource: "webhook",
		GithubOwner:   "me", GithubRepo: "proj",
	}).values

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

func TestCollectDispatchEnv_ExcludesTheDaemonHostPath(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "/opt/this-machine-only/bin/sparkwing")
	t.Setenv("SPARKWING_FOO", "kept")

	got := collectDispatchEnv(context.Background(), buildNode(t, "deploy", &stubJob{}), "run-7", nil).values

	if v, ok := got[wingdclient.HostBinEnv]; ok {
		t.Fatalf("%s=%q was captured into the dispatch snapshot; a replay elsewhere would exec it", wingdclient.HostBinEnv, v)
	}
	if got["SPARKWING_FOO"] != "kept" {
		t.Fatalf("the exclusion swept up the rest of the SPARKWING_ prefix: %v", got)
	}
}

func TestCollectDispatchEnv_NilRun(t *testing.T) {
	node := buildNode(t, "deploy", &stubJob{})
	got := collectDispatchEnv(context.Background(), node, "run-7", nil).values

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

func TestCollectDispatchEnv_DropsCredentialKeys(t *testing.T) {
	t.Setenv("SPARKWING_FOO", "from-env")
	t.Setenv("GITHUB_TOKEN", "gh-bearer")
	t.Setenv("SPARKWING_AGENT_TOKEN", "runner-bearer")
	t.Setenv("SPARKWING_SECRETS_KEY", "aead-key")
	t.Setenv("SPARKWING_DEPLOY_PASSWORD", "hunter2")

	node := buildNode(t, "deploy", &stubJob{}).
		Env("NODE_API_TOKEN", "node-bearer").
		Env("CUSTOM", "node-value")

	got := collectDispatchEnv(context.Background(), node, "run-7", nil)

	for k, v := range got.values {
		if strings.HasSuffix(k, "_TOKEN") || strings.Contains(k, "SECRET") ||
			strings.Contains(k, "PASSWORD") || strings.Contains(k, "KEY") ||
			strings.Contains(k, "CREDENTIAL") {
			t.Fatalf("credential-shaped key survived capture: %s=%q", k, v)
		}
	}
	for _, want := range []string{
		"GITHUB_TOKEN", "NODE_API_TOKEN", "SPARKWING_AGENT_TOKEN",
		"SPARKWING_DEPLOY_PASSWORD", "SPARKWING_SECRETS_KEY",
	} {
		if !slices.Contains(got.redactedKeys, want) {
			t.Fatalf("%s should be named in redacted keys: %v", want, got.redactedKeys)
		}
	}
	if !slices.IsSorted(got.redactedKeys) {
		t.Fatalf("redacted keys should be sorted: %v", got.redactedKeys)
	}
	if got.values["SPARKWING_FOO"] != "from-env" || got.values["CUSTOM"] != "node-value" {
		t.Fatalf("the deny list swept up ordinary keys: %v", got.values)
	}
}

func TestCollectDispatchEnv_MasksValues(t *testing.T) {
	t.Setenv("SPARKWING_ENDPOINT", "https://user:supersecret@example.test")

	m := secrets.NewMasker()
	m.Register("supersecret")
	ctx := secrets.WithMasker(context.Background(), m)

	node := buildNode(t, "deploy", &stubJob{}).Env("NODE_URL", "https://supersecret@example.test")
	got := collectDispatchEnv(ctx, node, "run-7", nil)

	for k, v := range got.values {
		if strings.Contains(v, "supersecret") {
			t.Fatalf("registered secret survived in %s=%q", k, v)
		}
	}
	if got.masked != 2 {
		t.Fatalf("masked count: got %d, want 2 (%v)", got.masked, got.values)
	}
}

func TestDispatchSnapshot_RecordsRedactedKeys(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "gh-bearer")
	t.Setenv("SPARKWING_PG_URL", "postgres://sparkwing:gh-bearer@db.example/sparkwing?sslmode=require")
	be := &captureBackend{}
	r := NewNodeExecutor(Backends{State: be})
	node := buildNode(t, "deploy", &stubJob{})

	if err := r.writeDispatchSnapshot(context.Background(), "run-1", node); err != nil {
		t.Fatalf("writeDispatchSnapshot: %v", err)
	}
	d := be.captured[0]
	if strings.Contains(string(d.EnvJSON), "gh-bearer") {
		t.Fatalf("env_json leaks a credential: %s", string(d.EnvJSON))
	}
	var keys []string
	if err := json.Unmarshal(d.RedactedKeys, &keys); err != nil {
		t.Fatalf("redacted_keys: %v (%s)", err, string(d.RedactedKeys))
	}
	if !slices.Contains(keys, "GITHUB_TOKEN") {
		t.Fatalf("redacted_keys should name GITHUB_TOKEN: %v", keys)
	}
	if !slices.Contains(keys, "SPARKWING_PG_URL") {
		t.Fatalf("redacted_keys should name SPARKWING_PG_URL: %v", keys)
	}
}

func TestCollectDispatchEnv_RedactsCredentialValues(t *testing.T) {
	t.Setenv("SPARKWING_CACHE_URL", "postgres://sparkwing:hunter2@db.example/sparkwing?sslmode=require")
	t.Setenv("SPARKWING_S3_ENDPOINT", "https://api.example/v1")

	node := buildNode(t, "deploy", &stubJob{}).
		Env("SERVICE_ACCOUNT", `{"kind":"sa","token":"ya29.body"}`).
		Env("SIGNING_MATERIAL", "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----").
		Env("UPSTREAM_HEADER", "Bearer abc.def.ghi")

	got := collectDispatchEnv(context.Background(), node, "run-7", nil)

	if v := got.values["SPARKWING_CACHE_URL"]; v != "postgres://redacted@db.example/sparkwing?sslmode=require" {
		t.Fatalf("DSN userinfo survived capture: %q", v)
	}
	if v := got.values["SPARKWING_S3_ENDPOINT"]; v != "https://api.example/v1" {
		t.Fatalf("a URL without userinfo should be untouched: %q", v)
	}
	for _, k := range []string{"SERVICE_ACCOUNT", "SIGNING_MATERIAL", "UPSTREAM_HEADER"} {
		if _, ok := got.values[k]; ok {
			t.Fatalf("credential-shaped value survived capture: %s=%q", k, got.values[k])
		}
		if !slices.Contains(got.redactedKeys, k) {
			t.Fatalf("%s should be named in redacted keys: %v", k, got.redactedKeys)
		}
	}
}

func TestCollectDispatchEnv_ExemptsWellKnownNonCredentialNames(t *testing.T) {
	t.Setenv("SPARKWING_REQUIRE_AUTH", "1")
	t.Setenv("SPARKWING_CACHE_ALLOW_UNAUTHENTICATED", "0")

	node := buildNode(t, "deploy", &stubJob{}).
		Env("GIT_AUTHOR_NAME", "Sparkwing Bot").
		Env("GOPRIVATE", "github.com/sparkwing-dev/*").
		Env("SSH_KEY_DIR", "/etc/sparkwing/ssh")

	got := collectDispatchEnv(context.Background(), node, "run-7", nil)

	for k, want := range map[string]string{
		"SPARKWING_REQUIRE_AUTH":                "1",
		"SPARKWING_CACHE_ALLOW_UNAUTHENTICATED": "0",
		"GIT_AUTHOR_NAME":                       "Sparkwing Bot",
		"GOPRIVATE":                             "github.com/sparkwing-dev/*",
		"SSH_KEY_DIR":                           "/etc/sparkwing/ssh",
	} {
		if got.values[k] != want {
			t.Fatalf("%s = %q, want %q (redacted: %v)", k, got.values[k], want, got.redactedKeys)
		}
	}
}
