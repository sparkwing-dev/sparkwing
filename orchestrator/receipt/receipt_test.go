package receipt_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// fixedRun returns a deterministic store.Run for hash-stability tests.
func fixedRun() *store.Run {
	start := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(15 * time.Second)
	return &store.Run{
		ID:           "run-fixed-1",
		Pipeline:     "deploy",
		Status:       "success",
		GitSHA:       "abc1234",
		Args:         map[string]string{"env": "prod", "tag": "v1.2.3"},
		PlanSnapshot: []byte(`{"nodes":["build","deploy"]}`),
		StartedAt:    start,
		FinishedAt:   &end,
	}
}

// node helper mints a finished node with the given times + outcome.
func node(id, outcome string, started time.Time, dur time.Duration, output []byte, deps ...string) *store.Node {
	finished := started.Add(dur)
	return &store.Node{
		NodeID:     id,
		Status:     "done",
		Outcome:    outcome,
		Deps:       deps,
		StartedAt:  &started,
		FinishedAt: &finished,
		Output:     output,
	}
}

func TestBuildReceipt_SuccessRun(t *testing.T) {
	t.Parallel()
	run := fixedRun()
	start := run.StartedAt
	nodes := []*store.Node{
		node("build", "success", start, 5*time.Second, []byte(`{"image":"foo:bar"}`)),
		node("deploy", "success", start.Add(5*time.Second), 7*time.Second, []byte(`{"ok":true}`), "build"),
	}

	r := receipt.BuildReceipt(run, nodes, 0.05, "profile:test (cost_per_runner_hour=$0.05)")

	if r.RunID != "run-fixed-1" || r.Pipeline != "deploy" || r.Status != "success" {
		t.Fatalf("header fields wrong: %+v", r)
	}
	if r.DurationMS != 15000 {
		t.Fatalf("duration_ms = %d, want 15000", r.DurationMS)
	}
	if len(r.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(r.Steps))
	}
	if r.Steps[0].Outcome != "success" || r.Steps[0].DurationMS != 5000 {
		t.Fatalf("step[0] = %+v", r.Steps[0])
	}
	if r.Identity.PipelineVersionHash == "" || !strings.HasPrefix(r.Identity.PipelineVersionHash, "sha256:") {
		t.Fatalf("pipeline_version_hash empty / wrong prefix: %q", r.Identity.PipelineVersionHash)
	}
	if r.Identity.InputsHash == "" || r.Identity.PlanHash == "" {
		t.Fatalf("identity hashes incomplete: %+v", r.Identity)
	}
	if r.Identity.OutputsHash["build"] == "" || r.Identity.OutputsHash["deploy"] == "" {
		t.Fatalf("outputs_hash missing entries: %+v", r.Identity.OutputsHash)
	}
	if r.Cost.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", r.Cost.Currency)
	}
	// 12 seconds × $0.05/hr = $0.000167 = 0 cents (rounded). Test the
	// nonzero-rate case in its own test below; this just proves the
	// rate plumbing reaches buildCost.
	if r.Cost.ComputeCents < 0 {
		t.Fatalf("compute_cents = %d, expected >= 0", r.Cost.ComputeCents)
	}
	if r.Cost.Settled {
		t.Fatalf("settled should default false until IMP-018")
	}
	if !strings.HasPrefix(r.ReceiptSHA, "sha256:") {
		t.Fatalf("receipt_sha empty / wrong prefix: %q", r.ReceiptSHA)
	}
}

func TestBuildReceipt_FailedRunWithOnFailureStep(t *testing.T) {
	t.Parallel()
	run := fixedRun()
	run.Status = "failed"
	start := run.StartedAt
	nodes := []*store.Node{
		node("build", "failed", start, 3*time.Second, nil),
		node("notify", "success", start.Add(3*time.Second), 1*time.Second, []byte(`{"sent":true}`), "build"),
	}

	r := receipt.BuildReceipt(run, nodes, 0, "profile:test")
	if r.Status != "failed" {
		t.Fatalf("status = %q, want failed", r.Status)
	}
	if r.Steps[0].Outcome != "failed" {
		t.Fatalf("step[0] outcome = %q", r.Steps[0].Outcome)
	}
	if r.Steps[1].Outcome != "success" {
		t.Fatalf("on-failure step outcome = %q, want success", r.Steps[1].Outcome)
	}
	// Build emitted no output (nil); notify did. Only notify should
	// appear in outputs_hash.
	if _, ok := r.Identity.OutputsHash["build"]; ok {
		t.Fatalf("build had no output; should not appear in outputs_hash")
	}
	if r.Identity.OutputsHash["notify"] == "" {
		t.Fatalf("notify output missing from hash map")
	}
}

func TestBuildReceipt_SkippedSteps(t *testing.T) {
	t.Parallel()
	run := fixedRun()
	start := run.StartedAt
	skipped := &store.Node{
		NodeID:       "deploy",
		Status:       "skipped",
		Outcome:      "skipped",
		StatusDetail: "skipped: pre-condition false",
	}
	nodes := []*store.Node{
		node("build", "success", start, 4*time.Second, []byte(`{"img":"x"}`)),
		skipped,
	}
	r := receipt.BuildReceipt(run, nodes, 1.0, "profile:test")
	if r.Steps[1].Outcome != "skipped" {
		t.Fatalf("step[1] outcome = %q, want skipped", r.Steps[1].Outcome)
	}
	if r.Steps[1].DurationMS != 0 {
		t.Fatalf("skipped step duration = %d, want 0", r.Steps[1].DurationMS)
	}
	if r.Steps[1].SkipReason != "skipped: pre-condition false" {
		t.Fatalf("skip_reason = %q", r.Steps[1].SkipReason)
	}
	// 4s of runner time × $1/hr = $0.001111 = 0 cents (rounded).
	// Skipped step contributes nothing.
	if r.Cost.ComputeCents != 0 {
		t.Fatalf("compute_cents = %d, want 0 (sub-cent rounded)", r.Cost.ComputeCents)
	}
}

func TestBuildReceipt_ZeroRateProfile(t *testing.T) {
	t.Parallel()
	run := fixedRun()
	start := run.StartedAt
	nodes := []*store.Node{
		node("build", "success", start, 1*time.Hour, nil),
	}
	r := receipt.BuildReceipt(run, nodes, 0, "profile:test")
	if r.Cost.ComputeCents != 0 {
		t.Fatalf("compute_cents = %d at rate=0, want 0", r.Cost.ComputeCents)
	}
	if r.Cost.Currency != "USD" {
		t.Fatalf("currency = %q", r.Cost.Currency)
	}
}

func TestBuildReceipt_NonZeroRateProduces_PositiveCost(t *testing.T) {
	t.Parallel()
	run := fixedRun()
	start := run.StartedAt
	// Two hours of runner time at $0.05/hr = $0.10 = 10 cents.
	nodes := []*store.Node{
		node("build", "success", start, 1*time.Hour, nil),
		node("deploy", "success", start.Add(1*time.Hour), 1*time.Hour, nil, "build"),
	}
	r := receipt.BuildReceipt(run, nodes, 0.05, "profile:test")
	if r.Cost.ComputeCents != 10 {
		t.Fatalf("compute_cents = %d, want 10 (2h × $0.05/hr)", r.Cost.ComputeCents)
	}
}

func TestBuildReceipt_ReceiptSHAStableAcrossRecomputes(t *testing.T) {
	t.Parallel()
	run := fixedRun()
	start := run.StartedAt
	nodes := []*store.Node{
		node("build", "success", start, 3*time.Second, []byte(`{"out":1}`)),
		node("deploy", "success", start.Add(3*time.Second), 2*time.Second, []byte(`{"out":2}`), "build"),
	}
	r1 := receipt.BuildReceipt(run, nodes, 0.05, "profile:test")
	r2 := receipt.BuildReceipt(run, nodes, 0.05, "profile:test")
	if r1.ReceiptSHA == "" {
		t.Fatalf("receipt_sha empty")
	}
	if r1.ReceiptSHA != r2.ReceiptSHA {
		t.Fatalf("receipt_sha unstable: %q vs %q", r1.ReceiptSHA, r2.ReceiptSHA)
	}
	// Sanity: changing the inputs flips the hash.
	run2 := *run
	run2.Args = map[string]string{"env": "stage"}
	r3 := receipt.BuildReceipt(&run2, nodes, 0.05, "profile:test")
	if r1.ReceiptSHA == r3.ReceiptSHA {
		t.Fatalf("receipt_sha did not change when inputs changed")
	}
}

func TestBuildReceipt_NilRunReturnsZeroValue(t *testing.T) {
	t.Parallel()
	r := receipt.BuildReceipt(nil, nil, 0, "")
	if r.RunID != "" || r.ReceiptSHA != "" {
		t.Fatalf("nil run should produce zero Receipt, got %+v", r)
	}
}
