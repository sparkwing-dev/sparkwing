package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func readReport(t *testing.T, f *cronsFixture, path string) []byte {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+f.reader)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d: %s", path, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

func TestTrends_BucketsRuns(t *testing.T) {
	f := newCronsFixture(t)
	st := f.store

	ctx := context.Background()
	now := time.Now()

	for i := range 3 {
		id := "r-succ-" + string(rune('a'+i))
		start := now.Add(-10 * time.Minute).Add(time.Duration(i) * time.Minute)
		if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "demo", Status: "running", StartedAt: start}}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}

	{
		id := "r-fail-1"
		if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "demo", Status: "running", StartedAt: now.Add(-2 * time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, id, "failed", "nope"); err != nil {
			t.Fatal(err)
		}
	}

	resp := readReport(t, f, "/api/v1/trends?hours=24")
	var body struct {
		Points []trendPointResponse `json:"points"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp)
	}
	if len(body.Points) < 2 {
		t.Fatalf("expected at least 2 buckets, got %d: %+v", len(body.Points), body.Points)
	}
	var totalPassed, totalFailed int
	for _, p := range body.Points {
		totalPassed += p.Passed
		totalFailed += p.Failed
	}
	if totalPassed != 3 {
		t.Errorf("passed=%d want 3", totalPassed)
	}
	if totalFailed != 1 {
		t.Errorf("failed=%d want 1", totalFailed)
	}
}

func TestTrends_AvgWaitMs(t *testing.T) {
	f := newCronsFixture(t)
	st := f.store

	ctx := context.Background()
	now := time.Now()

	start := now.Add(-5 * time.Minute)
	for i, waitMs := range []int{500, 1500} {
		id := "r-wait-" + string(rune('a'+i))
		if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "demo", Status: "running", CreatedAt: start.Add(-time.Duration(waitMs) * time.Millisecond), StartedAt: start}}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}

	resp := readReport(t, f, "/api/v1/trends?hours=24")
	var body struct {
		Points []trendPointResponse `json:"points"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp)
	}
	var got int64
	for _, p := range body.Points {
		if p.AvgWaitMs > got {
			got = p.AvgWaitMs
		}
	}
	if got != 1000 {
		t.Errorf("avg_wait_ms=%d want 1000 across %d buckets: %+v", got, len(body.Points), body.Points)
	}
}

func TestTrends_AvgWaitMs_ExcludesLegacyRows(t *testing.T) {
	f := newCronsFixture(t)
	st := f.store

	ctx := context.Background()
	now := time.Now()
	start := now.Add(-5 * time.Minute)

	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "r-real", Pipeline: "demo", Status: "running", CreatedAt: start.Add(-2 * time.Second), StartedAt: start}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, "r-real", "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "r-legacy", Pipeline: "demo", Status: "running", StartedAt: start}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, "r-legacy", "success", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(ctx, `UPDATE runs SET created_at = 0 WHERE id = ?`, "r-legacy"); err != nil {
		t.Fatal(err)
	}

	resp := readReport(t, f, "/api/v1/trends?hours=24")
	var body struct {
		Points []trendPointResponse `json:"points"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		t.Fatalf("decode trends: %v; body=%s", err, resp)
	}
	var got int64
	for _, p := range body.Points {
		if p.AvgWaitMs > got {
			got = p.AvgWaitMs
		}
	}
	if got < 1900 || got > 2100 {
		t.Errorf("avg_wait_ms=%d want ~2000 (legacy row should have been excluded): %+v", got, body.Points)
	}
}

func TestTrends_PipelineFilter(t *testing.T) {
	f := newCronsFixture(t)
	st := f.store

	ctx := context.Background()
	now := time.Now()
	for i, p := range []string{"alpha", "beta", "alpha"} {
		id := "r-" + p + "-" + string(rune('a'+i))
		if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: p, Status: "running", StartedAt: now.Add(-5 * time.Minute)}}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}

	resp := readReport(t, f, "/api/v1/trends?hours=24&pipeline=alpha")
	var body struct {
		Points   []trendPointResponse `json:"points"`
		Pipeline string               `json:"pipeline"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		t.Fatalf("decode trends: %v; body=%s", err, resp)
	}
	if body.Pipeline != "alpha" {
		t.Errorf("pipeline echo=%q want alpha", body.Pipeline)
	}
	total := 0
	for _, p := range body.Points {
		total += p.Total
	}
	if total != 2 {
		t.Errorf("alpha total=%d want 2 (filtered payload=%+v)", total, body)
	}
}

func TestTrendsRejectsStoreQueryFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	for _, failure := range []string{"query", "scan", "iteration"} {
		t.Run(failure, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := teststore.SeedRun(st, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run", Pipeline: "example", Status: "success", StartedAt: time.Now()}}); err != nil {
				t.Fatal(err)
			}
			raw, _, err := teststore.ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "reader"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
			if err != nil {
				t.Fatal(err)
			}
			handler := controller.New(st, nil).Handler()
			read := func() *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, "/api/v1/trends", nil)
				request.Header.Set("Authorization", "Bearer "+raw)
				output := httptest.NewRecorder()
				handler.ServeHTTP(output, request)
				return output
			}
			if output := read(); output.Code != http.StatusOK {
				t.Fatalf("healthy trends: %d %s", output.Code, output.Body)
			}
			var queries []string
			switch failure {
			case "query":
				queries = []string{`ALTER TABLE nodes RENAME TO original_nodes`}
			case "scan":
				queries = []string{
					`ALTER TABLE runs RENAME TO original_runs`,
					`CREATE VIEW runs AS
SELECT team, id, pipeline, status, created_at, started_at, 'not-an-integer' AS finished_at
  FROM original_runs`,
				}
			case "iteration":
				queries = []string{
					`ALTER TABLE nodes RENAME TO original_nodes`,
					`CREATE VIEW nodes AS SELECT 'default' AS team, 'run' AS run_id, 'memoized' AS outcome UNION ALL SELECT 'default', 'run', abs(-9223372036854775808)`,
				}
			}
			for _, query := range queries {
				if _, err := teststore.SQL(t, st).ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			output := read()
			if output.Code != http.StatusInternalServerError {
				t.Fatalf("%s failure returned status=%d body=%s", failure, output.Code, output.Body.String())
			}
		})
	}
}

type trendPointResponse struct {
	Bucket    string `json:"bucket"` // RFC3339 at the bucket boundary
	Total     int    `json:"total"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
	Cached    int    `json:"cached"`
	AvgDurMs  int64  `json:"avg_dur_ms"`
	P95DurMs  int64  `json:"p95_dur_ms"`
	AvgWaitMs int64  `json:"avg_wait_ms"`
}
