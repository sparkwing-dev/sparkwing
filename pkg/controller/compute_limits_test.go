package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
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

func seedRunNodeFor(t *testing.T, f creditsFixture, principal, runID, nodeID string) {
	t.Helper()
	ctx := store.WithCreatingPrincipal(context.Background(), principal)
	if err := f.store.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
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
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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

func TestComputeLimits_RefusesBadSiblingWithoutChangingGuards(t *testing.T) {
	f := newCreditsFixture(t, true)
	for range 20 {
		status, body := creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
			map[string]any{"limits": map[string]int64{
				store.ComputeLimitGlobalRunners:          7,
				store.ComputeLimitRunnerScaleStepCredits: math.MaxInt64,
			}})
		if status != http.StatusBadRequest {
			t.Fatalf("mixed write = %d: %s", status, body)
		}
	}
	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/compute-limits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("show = %d: %s", status, body)
	}
	var view struct {
		Limits map[string]int64 `json:"limits"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := view.Limits[store.ComputeLimitGlobalRunners]; got != 0 {
		t.Fatalf("the good half of a refused write set the global runner cap to %d", got)
	}
}

func TestComputeLimits_RefusesExplicitNullWithoutChangingGuards(t *testing.T) {
	f := newCreditsFixture(t, true)
	setComputeLimit(t, f, store.ComputeLimitGlobalRunners, 7)

	status, body := creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		map[string]any{"limits": map[string]any{store.ComputeLimitGlobalRunners: nil}})
	if status != http.StatusBadRequest {
		t.Fatalf("null write = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		map[string]any{"limits": nil})
	if status != http.StatusBadRequest {
		t.Fatalf("null limits = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		map[string]any{"LIMITS": map[string]any{store.ComputeLimitGlobalRunners: nil}})
	if status != http.StatusBadRequest {
		t.Fatalf("case-folded null write = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		json.RawMessage(`{"limits":{"max_global_runners":null},"limits":{"runner_alarm":3}}`))
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate null write = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodGet, f.url+"/api/v1/compute-limits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("show = %d: %s", status, body)
	}
	var view struct {
		Limits map[string]int64 `json:"limits"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := view.Limits[store.ComputeLimitGlobalRunners]; got != 7 {
		t.Fatalf("explicit null reset the global runner cap to %d", got)
	}
	status, body = creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		map[string]any{"LIMITS": map[string]any{store.ComputeLimitGlobalRunners: 8}})
	if status != http.StatusOK {
		t.Fatalf("case-folded valid write = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodPut, f.url+"/api/v1/compute-limits", f.admin,
		json.RawMessage(`{"limits":{"max_global_runners":9},"LIMITS":{"runner_alarm":3}}`))
	if status != http.StatusOK {
		t.Fatalf("duplicate valid write = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode duplicate result: %v", err)
	}
	if view.Limits[store.ComputeLimitGlobalRunners] != 9 ||
		view.Limits[store.ComputeLimitRunnerAlarm] != 3 {
		t.Fatalf("duplicate valid write did not merge: %v", view.Limits)
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
		seedRunNodeFor(t, f, "pool", runID, "build")
	}

	if n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil); err != nil || n == nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}

	// safety: the runner reads the refusal as a standing condition rather than
	// a transport failure, which is what keeps it polling instead of erroring.
	if _, err := c.ClaimNode(ctx, "pod-2", nil, time.Minute, nil); !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("the client read the refusal as %v, want ErrComputeLimit", err)
	}

	status, body, header := creditsRequestWithHeader(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-2", "lease_secs": 60})
	if status != http.StatusTooManyRequests {
		t.Fatalf("the second claim = %d, want 429: %s", status, body)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("the refusal carries no Retry-After")
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
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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
	seedRunNodeFor(t, f, "pool", "run-fan", "build")

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
	setComputeLimit(t, f, store.ComputeLimitGlobalRunsPerHour, 1)
	seedRunNode(t, f.store, "run-first", "build")

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/runs", f.admin,
		map[string]any{"id": "run-second", "pipeline": "demo", "status": "running"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("the run past the cap = %d, want 429: %s", status, body)
	}
}

func TestComputeLimits_CronBelowTheMinimumIntervalIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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

// safety: a refusal names the principal it refused, so recording it on another
// principal's run would put that name in a status its owner reads.
func TestComputeLimits_RefusalStaysInsideTheRefusedPrincipal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setComputeLimit(t, f, store.ComputeLimitConcurrentRunners, 1)
	seedRunNodeFor(t, f, "pool", "run-mine-a", "build")
	seedRunNodeFor(t, f, "pool", "run-mine-b", "build")
	seedRunNodeFor(t, f, "stranger", "run-theirs", "build")

	c := client.NewWithToken(f.url, nil, f.runner)
	if n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil); err != nil || n == nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}
	if _, err := c.ClaimNode(ctx, "pod-2", nil, time.Minute, nil); !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("the second claim = %v, want the guard", err)
	}

	if got := len(computeLimitEvents(t, f, "run-theirs")); got != 0 {
		t.Fatalf("another principal's run carries %d refusal events, want none", got)
	}
	if got := len(computeLimitEvents(t, f, "run-mine-b")); got != 1 {
		t.Fatalf("the refused principal's waiting run carries %d refusal events, want 1", got)
	}
}

func TestComputeLimits_GlobalRunnerGuardRefusesAnUnownedClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setComputeLimit(t, f, store.ComputeLimitGlobalRunners, 1)
	seedRunNode(t, f.store, "run-a", "build")
	seedRunNode(t, f.store, "run-b", "build")
	for _, runID := range []string{"run-a", "run-b"} {
		if err := f.store.MarkNodeReady(ctx, runID, "build"); err != nil {
			t.Fatalf("mark ready: %v", err)
		}
	}

	c := client.NewWithToken(f.url, nil, f.runner)
	if n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil); err != nil || n == nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}
	// safety: the global guard names no principal, so its refusal has no run of
	// its own and reaches the operator through the response and the log alone.
	_, err := c.ClaimNode(ctx, "pod-2", nil, time.Minute, nil)
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitGlobalRunners {
		t.Fatalf("the second claim = %v, want the global guard", err)
	}
}

// safety: a trigger creates the run it names, so the hourly guard has to see
// the triggering principal rather than an unowned run.
func TestComputeLimits_TriggeredRunsCountAgainstTheTriggeringPrincipal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	setComputeLimit(t, f, store.ComputeLimitRunsPerHour, 1)
	writer, _, err := f.store.CreateTokenWith(ctx, "pool", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite, controller.ScopeRunsControl, controller.ScopeRunsRead}, 0, time.Now().UTC(),
		store.TokenOptions{})
	if err != nil {
		t.Fatalf("mint a writer for the metered principal: %v", err)
	}

	body := map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]any{"source": "manual", "user": "operator"},
	}
	status, first := creditsRequest(t, http.MethodPost, f.url+"/api/v1/triggers", writer, body)
	if status != http.StatusAccepted {
		t.Fatalf("the first trigger = %d, want 202: %s", status, first)
	}
	status, second := creditsRequest(t, http.MethodPost, f.url+"/api/v1/triggers", writer, body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("the second trigger = %d, want 429: %s", status, second)
	}
	runs, err := f.store.ListRuns(ctx, store.RunFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want the refused one absent", len(runs))
	}
}

// safety: a retry storm is exactly the loop the hourly guard exists to bound,
// so a retry has to count against the principal that asked for it.
func TestComputeLimits_RetriesCountAgainstTheRetryingPrincipal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	writer, _, err := f.store.CreateTokenWith(ctx, "pool", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite, controller.ScopeRunsControl, controller.ScopeRunsRead}, 0, time.Now().UTC(),
		store.TokenOptions{})
	if err != nil {
		t.Fatalf("mint a writer for the metered principal: %v", err)
	}
	seedRunNodeFor(t, f, "pool", "run-source", "build")
	if err := f.store.FinishRun(ctx, "run-source", "failed", "boom"); err != nil {
		t.Fatalf("finish the source run: %v", err)
	}
	setComputeLimit(t, f, store.ComputeLimitRunsPerHour, 1)

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/runs/run-source/retry", writer, nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("a retry past the cap = %d, want 429: %s", status, body)
	}
	runs, err := f.store.ListRuns(ctx, store.RunFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want the retry absent", len(runs))
	}
	triggers, err := f.store.ListTriggers(ctx, store.TriggerFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	for _, trig := range triggers {
		if trig.RetryOf != "" {
			t.Fatalf("the refused retry left trigger %s behind", trig.ID)
		}
	}
}

// safety: a schedule's runs belong to the principal that armed it, which is
// what makes a hot cron count against that team rather than nobody.
func TestComputeLimits_CronLaunchesCarryTheArmingPrincipal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCronsFixture(t)
	ctx := context.Background()
	body := map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "nightly", "cron": "0 3 * * *"},
		},
	}
	f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusOK, nil)

	rows, err := f.store.ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("schedules = %d, want one", len(rows))
	}
	if rows[0].ArmedBy != "pusher" {
		t.Fatalf("armed_by = %q, want the arming principal", rows[0].ArmedBy)
	}
}

// safety: run-now is a launch like any other, so a guard has to refuse it with
// the same code every other surface answers.
func TestComputeLimits_CronRunNowAtTheCapAnswers429(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCronsFixture(t)
	ctx := context.Background()
	body := map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "nightly", "cron": "0 3 * * *"},
		},
	}
	f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusOK, nil)
	rows, err := f.store.ListCronSchedules(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list schedules = %d rows, %v", len(rows), err)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-already", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed a run: %v", err)
	}
	if err := f.store.SetComputeLimit(ctx, store.ComputeLimitGlobalRunsPerHour, 1); err != nil {
		t.Fatalf("set the guard: %v", err)
	}

	var refusal struct {
		Code  string `json:"code"`
		Limit string `json:"limit"`
	}
	f.call(http.MethodPost, "/api/v1/crons/"+rows[0].ID+"/run", f.writer, nil,
		http.StatusTooManyRequests, &refusal)
	if refusal.Code != controller.ComputeLimitRefusedCode ||
		refusal.Limit != store.ComputeLimitGlobalRunsPerHour {
		t.Fatalf("refusal = %+v, want the hourly guard named", refusal)
	}
	triggers, err := f.store.ListTriggers(ctx, store.TriggerFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) != 0 {
		t.Fatalf("the refused run-now left %d triggers behind", len(triggers))
	}
}

func TestComputeLimits_ShowsTheDerivedRunnerCap(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)

	_, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/compute-limits", f.readonly, nil)
	if bytes.Contains(body, []byte("derived_runner_cap")) {
		t.Fatalf("a controller with no per-principal guard reports a derived cap: %s", body)
	}

	setComputeLimit(t, f, store.ComputeLimitConcurrentRunners, 100)
	setComputeLimit(t, f, store.ComputeLimitRunnerScaleStepCredits, 5000)
	if _, err := f.store.GrantCredits(context.Background(), store.CreditGrantPaid,
		15000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/compute-limits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("show: status = %d: %s", status, body)
	}
	var view struct {
		Usage struct {
			DerivedRunnerCap   int64 `json:"derived_runner_cap"`
			RecentPaidMicro    int64 `json:"recent_paid_micro"`
			ScaleWindowSeconds int64 `json:"scale_window_seconds"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Usage.DerivedRunnerCap != 400 {
		t.Fatalf("derived cap = %d, want 400: %s", view.Usage.DerivedRunnerCap, body)
	}
	if view.Usage.RecentPaidMicro != 15000*store.MicroCreditsPerCredit {
		t.Fatalf("recent paid = %d, want the whole payment", view.Usage.RecentPaidMicro)
	}
	if view.Usage.ScaleWindowSeconds != int64(store.RunnerScaleWindow.Seconds()) {
		t.Fatalf("window = %ds, want %v", view.Usage.ScaleWindowSeconds, store.RunnerScaleWindow)
	}
}
