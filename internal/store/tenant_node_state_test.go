package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
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

func TestTenantEventsStayInTheRunsTeam(t *testing.T) {
	ctx := t.Context()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	if _, err := globex.AppendEventCharged(ctx, "replica", "run-a", "", "credits_blocked", []byte(`{}`)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign event append: %v", err)
	}
	if _, err := acme.AppendEventCharged(ctx, "replica", "run-a", "", "credits_blocked", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if events, err := globex.ListEventsAfter(ctx, "run-a", 0, 10); err != nil || len(events) != 0 {
		t.Fatalf("foreign events: %v, %v", events, err)
	}
	if events, err := acme.ListEventsAfter(ctx, "run-a", 0, 10); err != nil || len(events) != 1 {
		t.Fatalf("owner events: %v, %v", events, err)
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
