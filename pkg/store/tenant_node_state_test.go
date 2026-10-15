package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestTenantNodeWritesStayInTheRunsTeam(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.New(t))
	foreign := tenantFor(t, f.s, "globex")
	start := store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: acceptanceAttempt(0)}
	if _, err := foreign.TransitionNode(t.Context(), start); err == nil {
		t.Fatal("foreign tenant started the job")
	}
	if _, err := f.tenant.TransitionNode(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	attempt := metadataAttempt(t, f)
	for _, step := range []string{"compile", "test"} {
		if err := foreign.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: attempt, StepID: step, State: "running"}); err == nil {
			t.Fatal("foreign tenant wrote a step")
		}
		metadataStep(t, f, step, "running")
	}
	if steps, err := foreign.ListNodeSteps(t.Context(), f.ref.RunID); err != nil || len(steps) != 0 {
		t.Fatalf("foreign steps = %v, %v", steps, err)
	}
	if steps, err := f.tenant.ListNodeSteps(t.Context(), f.ref.RunID); err != nil || len(steps) != 2 {
		t.Fatalf("owner steps = %v, %v", steps, err)
	}
}

// A run's events belong to its team: another team's handle neither lists
// them nor appends a once-only event onto the run.
func TestTenantEventsStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")

	if _, err := globex.AppendEventOnce(ctx, "run-a", "", "credits_blocked", []byte(`{}`)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.AppendEventOnce = %v, want ErrNotFound", err)
	}
	if wrote, err := acme.AppendEventOnce(ctx, "run-a", "", "credits_blocked", []byte(`{}`)); err != nil || !wrote {
		t.Fatalf("acme.AppendEventOnce = %v, %v, want written", wrote, err)
	}
	if wrote, err := acme.AppendEventOnce(ctx, "run-a", "", "credits_blocked", []byte(`{}`)); err != nil || wrote {
		t.Fatalf("second acme.AppendEventOnce = %v, %v, want skipped", wrote, err)
	}
	if events, err := globex.ListEventsAfter(ctx, "run-a", 0, 10); err != nil || len(events) != 0 {
		t.Fatalf("globex.ListEventsAfter = %v, %v, want none", events, err)
	}
	if events, err := acme.ListEventsAfter(ctx, "run-a", 0, 10); err != nil || len(events) != 1 {
		t.Fatalf("acme.ListEventsAfter = %v, %v, want the one event", events, err)
	}
}

// Run control belongs to the run's team: another team's handle can neither
// cancel, retry-link, delete nor read the run, and leaves it as it was.
func TestTenantRunControlStaysInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")

	if _, err := globex.GetRun(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetRun = %v, want ErrNotFound", err)
	}
	if err := globex.DeleteRun(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.DeleteRun = %v, want ErrNotFound", err)
	}
	run, err := acme.GetRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("another team's delete removed the run: %v", err)
	}
	if run.RetriedAs != "" || run.FinishedAt != nil {
		t.Fatalf("another team's writes changed the run: %+v", run)
	}
	if err := acme.DeleteRun(ctx, "run-a"); err != nil {
		t.Fatalf("acme.DeleteRun: %v", err)
	}
	if _, err := acme.GetRun(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("acme.GetRun after delete = %v, want ErrNotFound", err)
	}
}

func TestTenantMetricsStayInTheRunsTeam(t *testing.T) {
	ctx := t.Context()
	f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
	acme := f.tenant
	globex := tenantFor(t, f.s, "globex")
	a := metricAuthorization(t, f, "metrics")

	sample := store.MetricSample{TS: time.Now(), CPUMillicores: 5}
	if err := globex.WriteMetric(ctx, a, sample); err == nil {
		t.Fatalf("globex.AddNodeMetricSample = %v, want refusal", err)
	}
	if err := acme.WriteMetric(ctx, a, sample); err != nil {
		t.Fatalf("acme.AddNodeMetricSample: %v", err)
	}
	if err := acme.WriteMetric(ctx, a, sample); err != nil {
		t.Fatalf("an identical retry: %v", err)
	}
	if got, err := globex.ListNodeMetricsPage(ctx, f.ref.RunID, f.ref.NodeID, time.Time{}, 0); err != nil || len(got) != 0 {
		t.Fatalf("globex.ListNodeMetricsPage = %v, %v, want none", got, err)
	}
	if got, err := acme.ListNodeMetricsPage(ctx, f.ref.RunID, f.ref.NodeID, time.Time{}, 0); err != nil || len(got) != 1 {
		t.Fatalf("acme.ListNodeMetricsPage = %v, %v, want the one sample", got, err)
	}
}

// The store-level finish acts on the default team, so a node of another
func TestTenantFinishRejectsForeignAndMissingJobs(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
	foreign := tenantFor(t, f.s, "foreign")
	change := store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1)}
	before, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.TransitionNode(t.Context(), change); err == nil {
		t.Fatal("foreign tenant finished job")
	}
	change.Ref.NodeID = "missing"
	if _, err := f.tenant.TransitionNode(t.Context(), change); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing job = %v", err)
	}
	after, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil || before.StateSeq != after.StateSeq || before.StateDigest != after.StateDigest || before.Generation != after.Generation || before.Attempt != after.Attempt || before.ClaimedBy != after.ClaimedBy || before.Status != after.Status {
		t.Fatalf("refusal changed job: %+v -> %+v, %v", before, after, err)
	}
}
