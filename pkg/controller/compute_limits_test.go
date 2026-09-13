package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func setComputeLimit(t *testing.T, f creditsFixture, name string, value int64) {
	t.Helper()
	status, body := creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		map[string]any{"limits": map[string]int64{name: value}})
	if status != http.StatusOK {
		t.Fatalf("set %s: status = %d: %s", name, status, body)
	}
}

func computeLimitEvents(t *testing.T, f creditsFixture, runID string) []store.Event {
	t.Helper()
	events, err := f.store.ListEventsAfter(context.Background(), runID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []store.Event
	for _, e := range events {
		if e.Kind == store.EventKindComputeLimitBlocked {
			out = append(out, e)
		}
	}
	return out
}

func TestComputeLimits_ShowAndSet(t *testing.T) {
	f := newCreditsFixture(t, true)

	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/compute-limits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("show: status = %d: %s", status, body)
	}
	var view struct {
		Limits map[string]int64 `json:"limits"`
		Usage  struct {
			Runners      int64 `json:"runners"`
			AlarmReached bool  `json:"alarm_reached"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, name := range store.ComputeLimitNames() {
		if view.Limits[name] != 0 {
			t.Fatalf("guard %s = %d on a fresh controller, want 0", name, view.Limits[name])
		}
	}
	if view.Usage.Runners != 0 {
		t.Fatalf("runners = %d, want 0", view.Usage.Runners)
	}

	setComputeLimit(t, f, store.ComputeLimitGlobalRunners, 12)
	status, body = creditsRequest(t, http.MethodGet, f.url+"/api/v1/compute-limits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("show after set: status = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Limits[store.ComputeLimitGlobalRunners] != 12 {
		t.Fatalf("global runners = %d, want 12", view.Limits[store.ComputeLimitGlobalRunners])
	}

	if status, _ := creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		map[string]any{"limits": map[string]int64{"max_dollars": 1}}); status != http.StatusBadRequest {
		t.Fatalf("an unknown guard = %d, want 400", status)
	}
	if status, _ := creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.readonly,
		map[string]any{"limits": map[string]int64{store.ComputeLimitGlobalRunners: 1}}); status != http.StatusForbidden {
		t.Fatalf("a reader setting a guard = %d, want 403", status)
	}
}

func TestComputeLimits_ClaimRefusedByTheRunnerGuard(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setComputeLimit(t, f, store.ComputeLimitConcurrentRunners, 1)
	c := client.NewWithToken(f.url, nil, f.runner)
	for _, runID := range []string{"run-one", "run-two"} {
		seedRunNode(t, f.store, runID, "build")
		if err := f.store.MarkNodeReady(ctx, runID, "build"); err != nil {
			t.Fatalf("mark ready: %v", err)
		}
	}

	if n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil); err != nil || n == nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-2", "lease_secs": 60})
	if status != http.StatusTooManyRequests {
		t.Fatalf("the second claim = %d, want 429: %s", status, body)
	}
	var refusal struct {
		Code     string `json:"code"`
		Limit    string `json:"limit"`
		Cap      int64  `json:"cap"`
		Observed int64  `json:"observed"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if refusal.Code != controller.ComputeLimitRefusedCode ||
		refusal.Limit != store.ComputeLimitConcurrentRunners {
		t.Fatalf("refusal = %+v, want the concurrent-runner guard", refusal)
	}
	if refusal.Cap != 1 || refusal.Observed != 1 {
		t.Fatalf("refusal = %+v, want cap 1 observed 1", refusal)
	}

	if got := len(computeLimitEvents(t, f, "run-two")); got != 1 {
		t.Fatalf("compute_limit_blocked events on the waiting run = %d, want 1", got)
	}
	if status, _ := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-2", "lease_secs": 60}); status != http.StatusTooManyRequests {
		t.Fatalf("a repeated claim = %d, want 429", status)
	}
	if got := len(computeLimitEvents(t, f, "run-two")); got != 1 {
		t.Fatalf("events after a second poll = %d, want the refusal recorded once", got)
	}
}

func TestComputeLimits_HeartbeatCancelsANodePastTheWallClockGuard(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-long", "build")
	if err := f.store.MarkNodeReady(ctx, "run-long", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	setComputeLimit(t, f, store.ComputeLimitRunSeconds, 60)
	if _, err := f.store.DB().Exec(
		`UPDATE runs SET started_at = ? WHERE id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), "run-long"); err != nil {
		t.Fatalf("age the run: %v", err)
	}

	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := c.HeartbeatNodeClaim(claimCtx, "run-long", "build", "pod-1", time.Minute, nil); err == nil {
		t.Fatal("the heartbeat past the wall-clock guard must be refused")
	}
	node, err := f.store.GetNode(ctx, "run-long", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.FailureReason != store.FailureComputeLimit {
		t.Fatalf("failure reason = %q, want %q", node.FailureReason, store.FailureComputeLimit)
	}
	if got := len(computeLimitEvents(t, f, "run-long")); got != 1 {
		t.Fatalf("compute_limit_blocked events = %d, want 1", got)
	}
}

func TestComputeLimits_NodeCreationRefusedPastThePerRunCap(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	setComputeLimit(t, f, store.ComputeLimitNodesPerRun, 1)
	seedRunNode(t, f.store, "run-fan", "build")

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/runs/run-fan/nodes", f.admin,
		map[string]any{"id": "fan-1", "status": "pending"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("the node past the cap = %d, want 429: %s", status, body)
	}
	if got := len(computeLimitEvents(t, f, "run-fan")); got != 1 {
		t.Fatalf("compute_limit_blocked events = %d, want 1", got)
	}
	nodes, err := f.store.ListNodes(ctx, "run-fan")
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want the refused one absent", len(nodes))
	}
}

func TestComputeLimits_RunCreationRefusedPastTheHourlyCap(t *testing.T) {
	f := newCreditsFixture(t, true)
	setComputeLimit(t, f, store.ComputeLimitRunsPerHour, 1)
	seedRunNode(t, f.store, "run-first", "build")

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/runs", f.admin,
		map[string]any{"id": "run-second", "pipeline": "demo", "status": "running"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("the run past the cap = %d, want 429: %s", status, body)
	}
}

func TestComputeLimits_CronBelowTheMinimumIntervalIsRefused(t *testing.T) {
	f := newCronsFixture(t)
	admin, _, err := f.store.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	f.call(http.MethodPut, "/api/v1/compute-limits", admin,
		map[string]any{"limits": map[string]int64{store.ComputeLimitCronSeconds: 900}},
		http.StatusOK, nil)

	body := map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "sweep", "cron": "*/5 * * * *"},
		},
	}
	f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusTooManyRequests, nil)

	body["schedules"] = []map[string]any{{"pipeline": "sweep", "cron": "0 * * * *"}}
	f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusOK, nil)
}
