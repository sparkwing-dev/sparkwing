package store_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMonitoringHTTPRoutesUseTheStoreDialect(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "http-agent-run", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "http-agent-run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "http-agent-run", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextReadyNode(ctx,
		store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"},
		"runner:http-agent:1", time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "http-trend-run", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "http-trend-run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "http-trend-run", "work", "cached", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "http-trend-run", "success", ""); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, slog.New(slog.DiscardHandler)).Handler())
	defer srv.Close()
	for _, test := range []struct {
		path, want string
	}{
		{path: "/api/v1/agents", want: `"name":"http-agent"`},
		{path: "/api/v1/trends?pipeline=demo", want: `"cached":1`},
	} {
		resp, err := http.Get(srv.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), test.want) {
			t.Fatalf("GET %s = %d %s, want body containing %s", test.path, resp.StatusCode, body, test.want)
		}
	}
}

func TestLegacyAgentClaimsUseTheStoreDialect(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "agent-run", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "agent-run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "agent-run", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextReadyNode(ctx,
		store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"},
		"runner:laptop:1", time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	claims, err := st.ListLegacyAgentClaims(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].RunID != "agent-run" || claims[0].ClaimedBy != "runner:laptop:1" {
		t.Fatalf("legacy claims = %+v", claims)
	}
}

func TestRunTrendsUseTheStoreDialect(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	for _, run := range []struct {
		id, pipeline, outcome string
	}{
		{id: "cached-run", pipeline: "alpha", outcome: "cached"},
		{id: "mixed-run", pipeline: "alpha", outcome: "success"},
		{id: "other-run", pipeline: "beta", outcome: "cached"},
	} {
		if err := st.CreateRun(ctx, store.Run{ID: run.id, Pipeline: run.pipeline, Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: run.id, NodeID: "work", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, run.id, "work", run.outcome, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, run.id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateRun(ctx, store.Run{ID: "empty-run", Pipeline: "alpha", Status: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	trends, err := st.ListRunTrends(ctx, now.Add(-time.Hour), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(trends) != 3 {
		t.Fatalf("trends = %+v, want three alpha runs", trends)
	}
	byID := map[string]store.RunTrend{}
	for _, trend := range trends {
		byID[trend.RunID] = trend
	}
	if !byID["cached-run"].Cached || byID["mixed-run"].Cached || byID["empty-run"].Cached {
		t.Fatalf("cached classification = %+v", byID)
	}
}
