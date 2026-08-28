package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedTrigger(t *testing.T, st *store.Store, id, pipeline string, at time.Time) {
	t.Helper()
	seedTriggerWithSource(t, st, id, pipeline, "", at)
}

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

func TestClaim_PipelineFilter_BasicInclude(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	seedTrigger(t, st, "t1", "other", now.Add(-10*time.Second))
	seedTrigger(t, st, "t2", "sample-build", now.Add(-1*time.Second))

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTriggerFor(context.Background(), []string{"sample-build"}, nil)
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got == nil || got.Pipeline != "sample-build" {
		t.Fatalf("claim: %+v", got)
	}

	any, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if any == nil || any.Pipeline != "other" {
		t.Fatalf("any claim: %+v", any)
	}
}

func TestClaim_PipelineFilter_Empty204(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

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
	orig, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if orig == nil || orig.Pipeline != "other" {
		t.Fatalf("orig claim: %+v", orig)
	}
}

func TestClaim_PipelineFilter_NilMeansAll(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

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

func TestClaim_SourceFilter_GithubWorkerSkipsManual(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	seedTriggerWithSource(t, st, "t1", "build", "manual", now)

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTriggerFor(context.Background(), nil, []string{"github"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got != nil {
		t.Fatalf("github-only worker claimed a manual trigger: %+v", got)
	}

	any, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if any == nil || any.TriggerSource != "manual" {
		t.Fatalf("unfiltered claim: %+v", any)
	}
}

func TestClaim_SourceFilter_WorkerClaimsMatchingSource(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	seedTriggerWithSource(t, st, "tg", "build", "github", now.Add(-10*time.Second))
	seedTriggerWithSource(t, st, "tm", "build", "manual", now.Add(-1*time.Second))

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTriggerFor(context.Background(), nil, []string{"manual", "schedule"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got == nil || got.TriggerSource != "manual" {
		t.Fatalf("expected manual trigger, got %+v", got)
	}

	github, err := c.ClaimTriggerFor(context.Background(), nil, []string{"github"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor github: %v", err)
	}
	if github == nil || github.TriggerSource != "github" {
		t.Fatalf("expected github trigger, got %+v", github)
	}
}

func TestClaim_SourceFilter_AndWithPipeline(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	seedTriggerWithSource(t, st, "t1", "build", "github", now.Add(-2*time.Second))
	seedTriggerWithSource(t, st, "t2", "other", "manual", now.Add(-1*time.Second))
	seedTriggerWithSource(t, st, "t3", "build", "manual", now)

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.ClaimTriggerFor(context.Background(), []string{"build"}, []string{"manual"})
	if err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got == nil || got.ID != "t3" {
		t.Fatalf("expected t3, got %+v", got)
	}
}
