package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type creditsFixture struct {
	url      string
	store    *store.Store
	admin    string
	runner   string
	prefix   string
	readonly string
}

func newCreditsFixture(t *testing.T, metered bool) creditsFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	runner, runnerTok, err := st.CreateTokenWith(context.Background(), "pool", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeRunsRead},
		0, now, store.TokenOptions{Metered: metered})
	if err != nil {
		t.Fatalf("runner token: %v", err)
	}
	readonly, _, err := st.CreateToken("viewer", store.TokenKindUser,
		[]string{controller.ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("reader token: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return creditsFixture{
		url: srv.URL, store: st,
		admin: admin, runner: runner, prefix: runnerTok.Prefix, readonly: readonly,
	}
}

func creditsRequest(t *testing.T, method, url, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func setNodeChargeWindow(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), runID, nodeID); err != nil {
		t.Fatalf("rewind the charge window: %v", err)
	}
}

func ageExhaustionStamp(t *testing.T, st *store.Store, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE sparkwing_meta SET value = ? WHERE key = 'credit_exhausted_at'`,
		at.UnixNano()); err != nil {
		t.Fatalf("age the exhaustion stamp: %v", err)
	}
}

func TestCredits_UnmeteredTokenClaimsAndIsNeverCharged(t *testing.T) {
	f := newCreditsFixture(t, false)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-plain", "build")
	if err := f.store.MarkNodeReady(ctx, "run-plain", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("claim on an unfunded controller must succeed for an unmetered token: %v", err)
	}
	if n == nil {
		t.Fatal("claim returned no node")
	}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := c.HeartbeatNodeClaim(claimCtx, "run-plain", "build", "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 0 {
		t.Fatalf("an unmetered token was charged: %+v", charges)
	}
}

func TestCredits_MeteredClaimRefusedOnAnEmptyBalance(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-broke", "build")
	if err := f.store.MarkNodeReady(ctx, "run-broke", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	_, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("claim error = %v, want ErrInsufficientCredits", err)
	}

	events, err := f.store.ListEventsAfter(ctx, "run-broke", 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen := 0
	for _, e := range events {
		if e.Kind == store.EventKindCreditsBlocked {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("credits_blocked events = %d, want 1", seen)
	}

	if _, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("second claim error = %v, want ErrInsufficientCredits", err)
	}
	events, err = f.store.ListEventsAfter(ctx, "run-broke", 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen = 0
	for _, e := range events {
		if e.Kind == store.EventKindCreditsBlocked {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("credits_blocked events after a second poll = %d, want 1", seen)
	}
}

func TestCredits_MeteredClaimChargesEachHeartbeat(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-paid", "build")
	if err := f.store.MarkNodeReady(ctx, "run-paid", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim on a funded ledger: %v", err)
	}

	// safety: the claim anchors the charge window, so rewinding it makes the
	// next heartbeat cover a known interval.
	setNodeChargeWindow(t, f.store, "run-paid", "build", time.Now().Add(-30*time.Second))

	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := c.HeartbeatNodeClaim(claimCtx, "run-paid", "build", "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	usage := creditChargesOfKind(charges, store.CreditChargeUsage)
	if len(usage) != 1 {
		t.Fatalf("usage charges = %d, want 1 beside the claim reservation", len(usage))
	}
	if usage[0].Seconds < 29 || usage[0].Seconds > 31 {
		t.Fatalf("charged %d seconds, want about 30", usage[0].Seconds)
	}
	if usage[0].TokenPrefix != f.prefix {
		t.Fatalf("charge token prefix = %q, want %q", usage[0].TokenPrefix, f.prefix)
	}
	if usage[0].RunID != "run-paid" || usage[0].NodeID != "build" {
		t.Fatalf("charge names %s/%s", usage[0].RunID, usage[0].NodeID)
	}
	if len(creditChargesOfKind(charges, store.CreditChargeReservation)) != 1 {
		t.Fatalf("the claim did not reserve: %+v", charges)
	}
}

func creditChargesOfKind(charges []store.CreditCharge, kind string) []store.CreditCharge {
	var out []store.CreditCharge
	for _, c := range charges {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func TestCredits_HeartbeatCancelsTheNodeAfterTheGracePeriod(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	floor, err := f.store.CreditClaimFloorMicro(ctx)
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	// safety: exactly one reservation, so the first heartbeat past it spends
	// the balance.
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantFree, floor, "", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := f.store.SetCreditGraceSeconds(ctx, 1); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-run-dry", "build")
	if err := f.store.MarkNodeReady(ctx, "run-run-dry", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})

	setNodeChargeWindow(t, f.store, "run-run-dry", "build", time.Now().Add(-30*time.Second))
	if err := c.HeartbeatNodeClaim(claimCtx, "run-run-dry", "build", "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	ageExhaustionStamp(t, f.store, time.Now().Add(-time.Minute))
	setNodeChargeWindow(t, f.store, "run-run-dry", "build", time.Now().Add(-2*time.Second))

	err = c.HeartbeatNodeClaim(claimCtx, "run-run-dry", "build", "pod-1", time.Minute, nil)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("heartbeat after the grace period = %v, want ErrLockHeld", err)
	}

	node, err := f.store.GetNode(ctx, "run-run-dry", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Status != "done" || node.Outcome != "failed" {
		t.Fatalf("node = %s/%s, want the cancellation to have landed", node.Status, node.Outcome)
	}
	if node.FailureReason != store.FailureCreditsExhausted {
		t.Fatalf("failure reason = %q, want %q", node.FailureReason, store.FailureCreditsExhausted)
	}
	if node.Claimed {
		t.Fatal("the cancelled node is still claimed, so it waits out its lease")
	}

	events, err := f.store.ListEventsAfter(ctx, "run-run-dry", 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Kind == store.EventKindCreditsExhausted {
			found = true
		}
	}
	if !found {
		t.Fatal("the run does not record why its node was cancelled")
	}
}

// A node that finishes between heartbeats pays for the seconds it ran and
// nothing more, because the finish settles the tail and refunds the rest of
// the claim reservation.
func TestCredits_NodeFinishSettlesTheLedgerToItsRuntime(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	before, err := f.store.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-brief", "build")
	if err := f.store.MarkNodeReady(ctx, "run-brief", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})

	// safety: the claim reserved through claim+60s; placing that instant 56
	// seconds out is a node claimed four seconds ago, finishing before the
	// first heartbeat would have fired.
	setNodeChargeWindow(t, f.store, "run-brief", "build",
		time.Now().Add(time.Duration(store.CreditClaimFloorSeconds-4)*time.Second))
	if err := c.StartNode(claimCtx, "run-brief", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	if err := c.FinishNode(claimCtx, "run-brief", "build", "success", "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	after, err := f.store.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	spent := before - after
	want := int64(4) * store.DefaultCreditRateMicro
	if diff := spent - want; diff > store.DefaultCreditRateMicro || diff < -store.DefaultCreditRateMicro {
		t.Fatalf("spent %d for four seconds of work, want %d within one second", spent, want)
	}
}

func TestCredits_RoutesShowGrantAndHistory(t *testing.T) {
	f := newCreditsFixture(t, false)

	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /credits = %d: %s", status, body)
	}
	var state struct {
		BalanceMicro       int64 `json:"balance_micro"`
		RateMicroPerSecond int64 `json:"rate_micro_per_second"`
		GraceSeconds       int64 `json:"grace_seconds"`
		MaxChargeSeconds   int64 `json:"max_charge_seconds"`
		MicroPerCredit     int64 `json:"micro_per_credit"`
		CreditsPerDollar   int64 `json:"credits_per_dollar"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.BalanceMicro != 0 {
		t.Fatalf("fresh balance = %d", state.BalanceMicro)
	}
	if state.RateMicroPerSecond != store.DefaultCreditRateMicro {
		t.Fatalf("rate = %d, want %d", state.RateMicroPerSecond, store.DefaultCreditRateMicro)
	}
	if state.GraceSeconds != store.DefaultCreditGraceSeconds {
		t.Fatalf("grace = %d, want %d", state.GraceSeconds, store.DefaultCreditGraceSeconds)
	}
	if state.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("charge cap = %d, want %d", state.MaxChargeSeconds, store.DefaultCreditMaxChargeSeconds)
	}
	if state.MicroPerCredit != store.MicroCreditsPerCredit || state.CreditsPerDollar != store.CreditsPerDollar {
		t.Fatalf("unit constants = %d/%d", state.MicroPerCredit, state.CreditsPerDollar)
	}

	status, _ = creditsRequest(t, http.MethodPost, f.url+"/api/v1/credits/grants", f.readonly,
		map[string]any{"kind": "paid", "amount_micro": 1000})
	if status != http.StatusForbidden {
		t.Fatalf("grant without admin = %d, want 403", status)
	}

	status, body = creditsRequest(t, http.MethodPost, f.url+"/api/v1/credits/grants", f.admin,
		map[string]any{"kind": "paid", "amount_micro": 1000 * store.MicroCreditsPerCredit, "reference": "pay_9"})
	if status != http.StatusCreated {
		t.Fatalf("grant = %d: %s", status, body)
	}

	status, body = creditsRequest(t, http.MethodPost, f.url+"/api/v1/credits/grants", f.admin,
		map[string]any{"kind": "gift", "amount_micro": 5})
	if status != http.StatusBadRequest {
		t.Fatalf("unknown grant kind = %d: %s", status, body)
	}

	status, body = creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits/history", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("history = %d: %s", status, body)
	}
	var history struct {
		Grants []struct {
			Kind        string `json:"kind"`
			AmountMicro int64  `json:"amount_micro"`
			Reference   string `json:"reference"`
			CreatedBy   string `json:"created_by"`
		} `json:"grants"`
		Charges []json.RawMessage `json:"charges"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Grants) != 1 {
		t.Fatalf("grants = %d, want 1", len(history.Grants))
	}
	if history.Grants[0].Reference != "pay_9" || history.Grants[0].CreatedBy != "root" {
		t.Fatalf("grant row = %+v", history.Grants[0])
	}
	if len(history.Charges) != 0 {
		t.Fatalf("charges = %d, want 0", len(history.Charges))
	}

	status, body = creditsRequest(t, http.MethodGet,
		f.url+"/api/v1/credits/history?limit=5000", f.readonly, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("history above the limit ceiling = %d: %s", status, body)
	}
	if !bytes.Contains(body, []byte("1000")) {
		t.Fatalf("the refusal does not name the ceiling: %s", body)
	}
}

func TestCredits_SetMeteredRouteMarksAnExistingToken(t *testing.T) {
	f := newCreditsFixture(t, false)
	ctx := context.Background()

	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/tokens/"+f.prefix+"/metered", f.readonly, map[string]any{"metered": true})
	if status != http.StatusForbidden {
		t.Fatalf("set-metered without admin = %d: %s", status, body)
	}

	status, body = creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/tokens/"+f.prefix+"/metered", f.admin, map[string]any{"metered": true})
	if status != http.StatusOK {
		t.Fatalf("set-metered = %d: %s", status, body)
	}
	var record struct {
		Prefix  string `json:"prefix"`
		Metered bool   `json:"metered"`
	}
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record.Prefix != f.prefix || !record.Metered {
		t.Fatalf("set-metered returned %+v", record)
	}
	metered, err := f.store.TokenMetered(ctx, f.prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("the marker did not reach the database")
	}

	status, body = creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/tokens/swu_notatoken/metered", f.admin, map[string]any{"metered": true})
	if status != http.StatusNotFound {
		t.Fatalf("set-metered on an unknown prefix = %d: %s", status, body)
	}
}
