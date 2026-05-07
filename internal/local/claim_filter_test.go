package local_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	controller "github.com/sparkwing-dev/sparkwing/v2/internal/local"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// seedTrigger is a thin helper for these tests; avoids re-typing the
// store.Trigger literal with only Pipeline varying.
func seedTrigger(t *testing.T, st *store.Store, id, pipeline string, at time.Time) {
	t.Helper()
	seedTriggerWithSource(t, st, id, pipeline, "", at)
}

// seedTriggerWithSource seeds a trigger with the given trigger_source.
func seedTriggerWithSource(t *testing.T, st *store.Store, id, pipeline, source string, at time.Time) {
	t.Helper()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID:            id,
		Pipeline:      pipeline,
		TriggerSource: source,
		CreatedAt:     at,
	}); err != nil {
		t.Fatalf("CreateTrigger %s: %v", id, err)
	}
}

// TestClaim_PipelineFilter_BasicInclude proves the advertised subset
// filter returns only matching pipelines. Older pending trigger for an
// unadvertised pipeline is skipped in favor of a newer matching one.
func TestClaim_PipelineFilter_BasicInclude(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// "other" is older (would win FIFO without filter); "my-app-build"
	// is newer but matches the worker's advertised set.
	now := time.Now()
	seedTrigger(t, st, "t1", "other", now.Add(-10*time.Second))
	seedTrigger(t, st, "t2", "my-app-build", now.Add(-1*time.Second))

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTriggerFor(context.Background(), []string{"my-app-build"}, nil)
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got == nil || got.Pipeline != "my-app-build" {
		t.Fatalf("claim: %+v", got)
	}

	// "other" remains claimable by a different worker.
	any, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if any == nil || any.Pipeline != "other" {
		t.Fatalf("any claim: %+v", any)
	}
}

// TestClaim_PipelineFilter_Empty204 proves a worker whose subset
// matches nothing in the queue sees 204 without claiming the
// unrelated pending rows.
func TestClaim_PipelineFilter_Empty204(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seedTrigger(t, st, "t1", "other", time.Now())

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTriggerFor(context.Background(), []string{"unrelated"}, nil)
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got != nil {
		t.Fatalf("expected 204 / nil, got %+v", got)
	}
	// The unmatched trigger is still pending for a different worker.
	orig, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if orig == nil || orig.Pipeline != "other" {
		t.Fatalf("orig claim: %+v", orig)
	}
}

// TestClaim_PipelineFilter_NilBackCompat proves omitting the filter
// (or passing nil) preserves the pre-filter "claim any" behavior so
// single-repo workers upgrading to the new client still work.
func TestClaim_PipelineFilter_NilBackCompat(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seedTrigger(t, st, "t1", "whatever", time.Now())

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if got == nil || got.Pipeline != "whatever" {
		t.Fatalf("claim: %+v", got)
	}
}

// trigger_source filter at claim time.

// TestClaim_SourceFilter_GithubWorkerSkipsManual proves that a worker
// advertising trigger_sources=["github"] does not claim a pending
// trigger with source="manual". The warm-runner trigger loop claims
// only github-stamped triggers; in-cluster workers claim only
// dev-driven ones.
func TestClaim_SourceFilter_GithubWorkerSkipsManual(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now()
	// A manual trigger is the only one in the queue.
	seedTriggerWithSource(t, st, "t1", "build", "manual", now)

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	// Worker that only handles github triggers should see 204.
	got, err := c.ClaimTriggerFor(context.Background(), nil, []string{"github"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got != nil {
		t.Fatalf("github-only worker claimed a manual trigger: %+v", got)
	}

	// The manual trigger is still claimable by an unfiltered worker.
	any, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if any == nil || any.TriggerSource != "manual" {
		t.Fatalf("unfiltered claim: %+v", any)
	}
}

// TestClaim_SourceFilter_WorkerClaimsMatchingSource proves a worker
// advertising trigger_sources=["manual","schedule"] claims a manual
// trigger and leaves a github trigger unclaimed.
func TestClaim_SourceFilter_WorkerClaimsMatchingSource(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now()
	// Seed two triggers: github (older, would win FIFO) and manual.
	seedTriggerWithSource(t, st, "tg", "build", "github", now.Add(-10*time.Second))
	seedTriggerWithSource(t, st, "tm", "build", "manual", now.Add(-1*time.Second))

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	// Worker that handles manual + schedule should skip the github trigger
	// and claim the manual one.
	got, err := c.ClaimTriggerFor(context.Background(), nil, []string{"manual", "schedule"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got == nil || got.TriggerSource != "manual" {
		t.Fatalf("expected manual trigger, got %+v", got)
	}

	// github trigger still pending for the warm-runner loop.
	github, err := c.ClaimTriggerFor(context.Background(), nil, []string{"github"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor github: %v", err)
	}
	if github == nil || github.TriggerSource != "github" {
		t.Fatalf("expected github trigger, got %+v", github)
	}
}

// TestClaim_SourceFilter_AndWithPipeline proves that both pipeline and
// source filters apply simultaneously (AND semantics). A trigger matching
// only one filter is not claimed.
func TestClaim_SourceFilter_AndWithPipeline(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now()
	// right-pipeline, wrong-source
	seedTriggerWithSource(t, st, "t1", "build", "github", now.Add(-2*time.Second))
	// right-source, wrong-pipeline
	seedTriggerWithSource(t, st, "t2", "other", "manual", now.Add(-1*time.Second))
	// both match
	seedTriggerWithSource(t, st, "t3", "build", "manual", now)

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	// Filter: pipeline=build AND source=manual. Only t3 matches.
	got, err := c.ClaimTriggerFor(context.Background(), []string{"build"}, []string{"manual"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got == nil || got.ID != "t3" {
		t.Fatalf("expected t3, got %+v", got)
	}
}
