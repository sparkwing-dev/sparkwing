package controller_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newBudgetServer(t *testing.T, b controller.RequestBudget) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// safety: a route scoped to a run answers 404 at the team boundary before its
	// budget when the run is missing, so the budgets are measured against a real one.
	if err := st.CreateRun(context.Background(), store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	ts := httptest.NewServer(controller.New(st, nil).WithRequestBudget(b).Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func postAsRunner(t *testing.T, url, runner string, body any) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, jsonBody(t, body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if runner != "" {
		req.Header.Set(store.RunnerIdentityHeader, runner)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func claimAs(t *testing.T, base, runner string) *http.Response {
	t.Helper()
	return postAsRunner(t, base+"/api/v1/nodes/claim", runner, map[string]any{"holder_id": runner})
}

func TestRequestBudget_ShedsClaimsPastTheRunnerBudget(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{ClaimsPerMinute: 2})

	for i := range 2 {
		resp := claimAs(t, base, "runner-1")
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusNoContent {
			t.Fatalf("claim %d status=%d want 204", i, status)
		}
	}

	resp := claimAs(t, base, "runner-1")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("claim past the budget status=%d want 429", resp.StatusCode)
	}
	first, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || first <= 0 {
		t.Fatalf("Retry-After=%q want a positive whole number of seconds", resp.Header.Get("Retry-After"))
	}

	// safety: a caller that keeps knocking while empty must be sent further away,
	// or the refusal costs the controller as much as the request would have.
	pressured := claimAs(t, base, "runner-1")
	defer func() { _ = pressured.Body.Close() }()
	second, err := strconv.Atoi(pressured.Header.Get("Retry-After"))
	if err != nil || second < first {
		t.Errorf("Retry-After went %ds then %ds; it must not shorten under pressure", first, second)
	}
}

func TestRequestBudget_BudgetsRunnersSharingATokenApart(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{ClaimsPerMinute: 1})

	spent := claimAs(t, base, "runner-1")
	_ = spent.Body.Close()
	drained := claimAs(t, base, "runner-1")
	status := drained.StatusCode
	_ = drained.Body.Close()
	if status != http.StatusTooManyRequests {
		t.Fatalf("second claim from the same runner status=%d want 429", status)
	}

	peer := claimAs(t, base, "runner-2")
	defer func() { _ = peer.Body.Close() }()
	if peer.StatusCode != http.StatusNoContent {
		t.Fatalf("peer runner status=%d want 204; one runner's loop starved its peers", peer.StatusCode)
	}
}

func TestRequestBudget_NeverShedsTheAgentLivenessHeartbeat(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{HeartbeatsPerMinute: 1})

	for i := range 8 {
		resp, err := http.Post(base+"/api/v1/agents/agent-1/heartbeat", "application/json",
			bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusTooManyRequests {
			t.Fatalf("agent liveness heartbeat %d was shed; losing it kills the agent and its nodes", i)
		}
	}
}

func TestRequestBudget_ShedsNodeHeartbeatsPastTheRunnerBudget(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{HeartbeatsPerMinute: 1})
	url := base + "/api/v1/runs/run-1/nodes/node-1/heartbeat"

	first := postAsRunner(t, url, "runner-1", map[string]any{"holder_id": "runner-1"})
	_ = first.Body.Close()

	resp := postAsRunner(t, url, "runner-1", map[string]any{"holder_id": "runner-1"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("heartbeat past the budget status=%d want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("shed heartbeat carried no Retry-After")
	}
}

func TestRequestBudget_DefaultsToUnlimited(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(ts.Close)

	for i := range 40 {
		resp := claimAs(t, ts.URL, "runner-1")
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusNoContent {
			t.Fatalf("claim %d status=%d want 204 on a controller given no budget", i, status)
		}
	}
}

func TestRequestBudget_LocalExecutionBudgetsNothing(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).
		WithRequestBudget(controller.RequestBudget{ClaimsPerMinute: 1, HeartbeatsPerMinute: 1}).
		WithLocalExecution()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for i := range 5 {
		resp := claimAs(t, ts.URL, "runner-1")
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusNoContent {
			t.Fatalf("claim %d status=%d; a host's own controller shares one bucket and must not budget", i, status)
		}
	}
}

// The recommendations must clear the cadence the shipped runners actually use:
// a pool runner claims every 500ms and a node heartbeat runs every 3s.
func TestRequestBudget_RecommendationsClearTheShippedCadence(t *testing.T) {
	beatsAMinute := int(time.Minute / (3 * time.Second))
	if controller.CompliantClaimPollsPerMinute() != int(time.Minute/controller.ClaimPollInterval) {
		t.Errorf("CompliantClaimPollsPerMinute=%d; a %s poller spends a minute of them",
			controller.CompliantClaimPollsPerMinute(), controller.ClaimPollInterval)
	}
	if controller.RecommendedHeartbeatsPerMinute < beatsAMinute*2 {
		t.Errorf("RecommendedHeartbeatsPerMinute=%d; a 3s heartbeat spends %d a minute",
			controller.RecommendedHeartbeatsPerMinute, beatsAMinute)
	}
}

func TestRequestBudget_SharedTokenFleetStaysAliveUnderTheRecommendedBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	base := newBudgetServer(t, controller.RequestBudget{
		ClaimsPerMinute:     controller.CompliantClaimPollsPerMinute() * 4,
		HeartbeatsPerMinute: controller.RecommendedHeartbeatsPerMinute,
	})

	const (
		fleet           = 50
		claimsPerRunner = 10
	)
	for runner := range fleet {
		name := "runner-" + strconv.Itoa(runner)
		for i := range claimsPerRunner {
			resp := claimAs(t, base, name)
			status := resp.StatusCode
			_ = resp.Body.Close()
			if status != http.StatusNoContent {
				t.Fatalf("%s claim %d status=%d; fifty runners sharing a token must not shed each other",
					name, i, status)
			}
		}
		beat, err := http.Post(base+"/api/v1/agents/"+name+"/heartbeat", "application/json",
			bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatalf("%s heartbeat: %v", name, err)
		}
		status := beat.StatusCode
		_ = beat.Body.Close()
		if status == http.StatusTooManyRequests {
			t.Fatalf("%s liveness heartbeat was shed under fleet load", name)
		}
	}
}

// A runner names itself on the claim routes, so each new name buys a fresh
// budget; the number of names one caller may hold at once is capped, and a
// name already held keeps working past the cap.
func TestRequestBudget_CapsTheRunnerNamesOneCallerHolds(t *testing.T) {
	base := newBudgetServer(t, controller.RequestBudget{ClaimsPerMinute: 100, RunnersPerToken: 2})
	for _, runner := range []string{"runner-1", "runner-2"} {
		resp := claimAs(t, base, runner)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s status=%d want 204", runner, resp.StatusCode)
		}
	}
	third := claimAs(t, base, "runner-3")
	defer func() { _ = third.Body.Close() }()
	body, _ := io.ReadAll(third.Body)
	if third.StatusCode != http.StatusTooManyRequests || !bytes.Contains(body, []byte("2 runner")) {
		t.Fatalf("a third runner name status=%d body=%s, want 429 naming the cap of 2", third.StatusCode, body)
	}
	again := claimAs(t, base, "runner-1")
	defer func() { _ = again.Body.Close() }()
	if again.StatusCode != http.StatusNoContent {
		t.Fatalf("a runner name already held status=%d want 204", again.StatusCode)
	}
}
