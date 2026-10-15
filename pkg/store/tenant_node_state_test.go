package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A node's writes and its steps belong to its run's team: every write
// another team's handle aims at the node reads as not found and leaves it
// as it was, and that handle lists none of its steps.
func TestTenantNodeWritesStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	writes := map[string]func(*store.Tenant) error{
		"StartNode":               func(tn *store.Tenant) error { return tn.StartNode(ctx, "run-a", "build") },
		"SetNodeArtifactManifest": func(tn *store.Tenant) error { return tn.SetNodeArtifactManifest(ctx, "run-a", "build", "sha256:x") },
	}
	for name, write := range writes {
		if err := write(globex); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("globex.%s = %v, want ErrNotFound", name, err)
		}
	}
	usage := store.NodeUsage{CPUTime: time.Second, Wall: time.Second}
	if err := globex.AddNodeUsage(ctx, "run-a", "build", usage); err != nil {
		t.Errorf("globex.AddNodeUsage = %v, want the no-op a missing node gets", err)
	}
	n, err := acme.GetNode(ctx, "run-a", "build")
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != "pending" || n.Summary != "" || len(n.Annotations) != 0 || n.ArtifactManifest != "" || n.CPUNanos != 0 {
		t.Fatalf("another team's writes changed the node: %+v", n)
	}
	for name, write := range writes {
		if err := write(acme); err != nil {
			t.Errorf("acme.%s: %v", name, err)
		}
	}
	if steps, err := globex.ListNodeSteps(ctx, "run-a"); err != nil || len(steps) != 0 {
		t.Fatalf("globex.ListNodeSteps = %v, %v, want none", steps, err)
	}
	if steps, err := acme.ListNodeSteps(ctx, "run-a"); err != nil || len(steps) != 2 {
		t.Fatalf("acme.ListNodeSteps = %v, %v, want compile and test", steps, err)
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
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := acme.StartNode(ctx, "run-a", "build"); err != nil {
		t.Fatal(err)
	}

	sample := store.MetricSample{TS: time.Now(), CPUMillicores: 5}
	if err := globex.AddNodeMetricSample(ctx, "run-a", "build", sample); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.AddNodeMetricSample = %v, want ErrNotFound", err)
	}
	if err := acme.AddNodeMetricSample(ctx, "run-a", "build", sample); err != nil {
		t.Fatalf("acme.AddNodeMetricSample: %v", err)
	}
	if err := acme.AddNodeMetricSample(ctx, "run-a", "build", sample); err != nil {
		t.Fatalf("an identical retry: %v", err)
	}
	if got, err := globex.ListNodeMetricsPage(ctx, "run-a", "build", time.Time{}, 0); err != nil || len(got) != 0 {
		t.Fatalf("globex.ListNodeMetricsPage = %v, %v, want none", got, err)
	}
	if got, err := acme.ListNodeMetricsPage(ctx, "run-a", "build", time.Time{}, 0); err != nil || len(got) != 1 {
		t.Fatalf("acme.ListNodeMetricsPage = %v, %v, want the one sample", got, err)
	}
}

// The store-level finish acts on the default team, so a node of another
// team is refused before its output is stored, and stays as it was.
func TestStoreFinishRefusesAnotherTeamsNode(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	seedTenantRun(t, acme, "run-a", "deploy")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	err := st.FinishNodeWithReason(ctx, "run-a", "build", "success", "", []byte(`{"ok":true}`), store.FailureUnknown, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("FinishNodeWithReason on acme's node = %v, want ErrNotFound", err)
	}
	n, err := acme.GetNode(ctx, "run-a", "build")
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != "running" || n.OutputRef != nil {
		t.Fatalf("acme's node = status %q output %+v; want it running with no output", n.Status, n.OutputRef)
	}
	if err := acme.FinishNodeWithOutputRef(ctx, "run-a", "missing", "success", "", nil, store.FailureUnknown, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("finishing a missing node = %v, want ErrNotFound", err)
	}
}
