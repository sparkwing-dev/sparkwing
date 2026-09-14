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
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a node whose plan pins no cpu resolves to one core, which the
// smallest class of the default rate table covers, so that is the price these
// fixtures are billed at.
var unpinnedNodeRateMicro = store.DefaultCreditRateTable(store.DefaultCreditRateMicro).RateFor(1)

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

func creditsRequestWithHeader(
	t *testing.T, method, url, token string, body any,
) (int, []byte, http.Header) {
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
	return resp.StatusCode, out, resp.Header
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

// safety: the grace clock a cancellation runs on is the node's own, so a test
// reaches the deadline by moving that instant rather than the ledger stamp.
func ageNodeExhaustionAnchor(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE nodes SET credit_exhausted_anchor = ? WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), runID, nodeID); err != nil {
		t.Fatalf("age the node's exhaustion anchor: %v", err)
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
	floor = unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	// safety: exactly one reservation, so the first heartbeat past it spends
	// the balance.
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantFree, floor, "", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	grace := int64(1)
	if _, err := f.store.SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
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

	// safety: the node is inside the minute its claim reserved and paid for, so
	// the spent balance alone must not stop it.
	if err := c.HeartbeatNodeClaim(claimCtx, "run-run-dry", "build", "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	node, err := f.store.GetNode(ctx, "run-run-dry", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Status == "done" {
		t.Fatal("the node was cancelled inside the reservation its claim paid for")
	}
	ageExhaustionStamp(t, f.store, time.Now().Add(-time.Minute))
	ageNodeExhaustionAnchor(t, f.store, "run-run-dry", "build", time.Now().Add(-time.Minute))
	setNodeChargeWindow(t, f.store, "run-run-dry", "build", time.Now().Add(-2*time.Second))

	err = c.HeartbeatNodeClaim(claimCtx, "run-run-dry", "build", "pod-1", time.Minute, nil)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("heartbeat after the grace period = %v, want ErrLockHeld", err)
	}

	node, err = f.store.GetNode(ctx, "run-run-dry", "build")
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
	want := int64(4) * unpinnedNodeRateMicro
	if diff := spent - want; diff > unpinnedNodeRateMicro || diff < -unpinnedNodeRateMicro {
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

func creditSettings(t *testing.T, f creditsFixture, method string, body any) (int, creditSettingsView) {
	t.Helper()
	status, raw := creditsRequest(t, method, f.url+"/api/v1/credits/settings", f.admin, body)
	var view creditSettingsView
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("decode settings: %v: %s", err, raw)
		}
	}
	return status, view
}

type creditSettingsView struct {
	RateMicroPerSecond int64            `json:"rate_micro_per_second"`
	RateTable          []creditRateView `json:"rate_table"`
	RateTableSet       bool             `json:"rate_table_set"`
	WarmCPUClassCores  int64            `json:"warm_cpu_class_cores"`
	GraceSeconds       int64            `json:"grace_seconds"`
	MaxChargeSeconds   int64            `json:"max_charge_seconds"`
	MicroPerCredit     int64            `json:"micro_per_credit"`
	CreditsPerDollar   int64            `json:"credits_per_dollar"`
}

type creditRateView struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

func (v creditSettingsView) rateFor(cores int64) int64 {
	for _, entry := range v.RateTable {
		if entry.Cores == cores {
			return entry.MicroPerSecond
		}
	}
	return 0
}

func TestCreditSettings_ReadsTheDefaultsAndSetsEachValue(t *testing.T) {
	f := newCreditsFixture(t, false)

	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	if view.RateMicroPerSecond != store.DefaultCreditRateMicro ||
		view.GraceSeconds != store.DefaultCreditGraceSeconds ||
		view.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("settings = %+v, want the package defaults", view)
	}
	if view.MicroPerCredit != store.MicroCreditsPerCredit || view.CreditsPerDollar != store.CreditsPerDollar {
		t.Fatalf("unit constants = %d/%d", view.MicroPerCredit, view.CreditsPerDollar)
	}

	status, view = creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_micro_per_second": 30_000, "grace_seconds": 0, "max_charge_seconds": 45,
	})
	if status != http.StatusOK {
		t.Fatalf("PUT settings = %d", status)
	}
	if view.RateMicroPerSecond != 30_000 || view.GraceSeconds != 0 || view.MaxChargeSeconds != 45 {
		t.Fatalf("settings after the write = %+v", view)
	}
	grace, err := f.store.CreditGraceSeconds(context.Background())
	if err != nil {
		t.Fatalf("read grace: %v", err)
	}
	if grace != 0 {
		t.Fatalf("stored grace = %d, want 0", grace)
	}
}

func TestCreditSettings_LeavesUnnamedValuesAlone(t *testing.T) {
	f := newCreditsFixture(t, false)

	if status, _ := creditSettings(t, f, http.MethodPut,
		map[string]any{"grace_seconds": 0}); status != http.StatusOK {
		t.Fatalf("PUT grace only = %d", status)
	}
	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	if view.GraceSeconds != 0 {
		t.Fatalf("grace = %d, want 0", view.GraceSeconds)
	}
	if view.RateMicroPerSecond != store.DefaultCreditRateMicro ||
		view.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("a one-field write moved the other settings: %+v", view)
	}
}

func TestCreditSettings_RefusesValuesTheLedgerCannotPrice(t *testing.T) {
	f := newCreditsFixture(t, false)

	for name, body := range map[string]map[string]any{
		"no field named": {},
		"rate at zero":   {"rate_micro_per_second": 0},
		"negative rate":  {"rate_micro_per_second": -1},
		"negative grace": {"grace_seconds": -1},
		"cap under the heartbeat cadence": {
			"max_charge_seconds": store.MinCreditMaxChargeSeconds - 1,
		},
		"cap past the ceiling": {
			"max_charge_seconds": int64(store.MaxCreditMaxChargeSeconds) + 1,
		},
		"rate past the ceiling": {
			"rate_micro_per_second": int64(store.MaxCreditRateMicro) + 1,
		},
	} {
		if status, _ := creditSettings(t, f, http.MethodPut, body); status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, status)
		}
	}

	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	if view.RateMicroPerSecond != store.DefaultCreditRateMicro ||
		view.GraceSeconds != store.DefaultCreditGraceSeconds ||
		view.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("a refused write moved a setting: %+v", view)
	}
}

func TestCreditSettings_RefusesAWriteWhoseOtherFieldIsBad(t *testing.T) {
	f := newCreditsFixture(t, false)

	status, _ := creditSettings(t, f, http.MethodPut,
		map[string]any{"grace_seconds": 0, "rate_micro_per_second": -5})
	if status != http.StatusBadRequest {
		t.Fatalf("mixed write = %d, want 400", status)
	}
	grace, err := f.store.CreditGraceSeconds(context.Background())
	if err != nil {
		t.Fatalf("read grace: %v", err)
	}
	if grace != store.DefaultCreditGraceSeconds {
		t.Fatalf("grace = %d; the good half of a refused write was applied", grace)
	}
}

func TestCreditSettings_ReadNeedsRunsReadAndWriteNeedsAdmin(t *testing.T) {
	f := newCreditsFixture(t, false)

	status, _ := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits/settings", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("read with runs.read = %d, want 200", status)
	}
	status, _ = creditsRequest(t, http.MethodPut, f.url+"/api/v1/credits/settings", f.readonly,
		map[string]any{"grace_seconds": 0})
	if status != http.StatusForbidden {
		t.Fatalf("write without admin = %d, want 403", status)
	}
}

// An installation that never set a table reads one price for every class, so
// its bill is what the single rate charged on its own.
func TestCreditSettings_ReadsTheDefaultRateLadder(t *testing.T) {
	f := newCreditsFixture(t, false)

	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	if view.RateTableSet {
		t.Fatal("a controller that set no table reports one")
	}
	want := store.DefaultCreditRateTable(store.DefaultCreditRateMicro)
	if len(view.RateTable) != len(want) {
		t.Fatalf("the settings route named %d classes, want %d", len(view.RateTable), len(want))
	}
	for i, entry := range view.RateTable {
		if entry.Cores != want[i].Cores || entry.MicroPerSecond != want[i].MicroPerSecond {
			t.Fatalf("the %d-core class costs %d, want %+v", entry.Cores, entry.MicroPerSecond, want[i])
		}
	}
}

func TestCreditSettings_TakesTheRateTableAsAListOrAsAnObject(t *testing.T) {
	f := newCreditsFixture(t, false)

	status, view := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": []map[string]any{
			{"cores": 2, "micro_per_second": 10_000},
			{"cores": 8, "micro_per_second": 36_667},
			{"cores": 4, "micro_per_second": 20_000},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT a list = %d", status)
	}
	if len(view.RateTable) != 3 || view.RateTable[0].Cores != 2 || view.RateTable[2].Cores != 8 {
		t.Fatalf("rate table = %+v, want three classes smallest first", view.RateTable)
	}
	if view.RateMicroPerSecond != 20_000 {
		t.Fatalf("single rate = %d, want the four-core price 20000", view.RateMicroPerSecond)
	}

	status, view = creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 11_000, "4": 22_000},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT an object = %d", status)
	}
	if view.rateFor(2) != 11_000 || view.rateFor(4) != 22_000 || len(view.RateTable) != 2 {
		t.Fatalf("rate table after the object write = %+v", view.RateTable)
	}
	if view.GraceSeconds != store.DefaultCreditGraceSeconds {
		t.Fatalf("writing the table moved the grace period to %d", view.GraceSeconds)
	}
}

func TestCreditSettings_RefusesARateTableTheLedgerCannotPrice(t *testing.T) {
	f := newCreditsFixture(t, false)

	for name, body := range map[string]map[string]any{
		"no class":   {"rate_table": []map[string]any{}},
		"zero cores": {"rate_table": []map[string]any{{"cores": 0, "micro_per_second": 1}}},
		"zero rate":  {"rate_table": []map[string]any{{"cores": 2, "micro_per_second": 0}}},
		"a class twice": {"rate_table": []map[string]any{
			{"cores": 2, "micro_per_second": 1}, {"cores": 2, "micro_per_second": 2},
		}},
		"a key that is not a core count": {"rate_table": map[string]int64{"large": 1}},
		"neither a list nor an object":   {"rate_table": "2=10000"},
	} {
		if status, _ := creditSettings(t, f, http.MethodPut, body); status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, status)
		}
	}

	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	want := store.DefaultCreditRateTable(store.DefaultCreditRateMicro)
	for i, entry := range view.RateTable {
		if entry.MicroPerSecond != want[i].MicroPerSecond {
			t.Fatalf("a refused write priced the %d-core class at %d", entry.Cores, entry.MicroPerSecond)
		}
	}
}

func TestCredits_HistoryNamesTheClassAndRateEachChargeWasBilledAt(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-classed", "build")
	if err := f.store.MarkNodeReady(ctx, "run-classed", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	setNodeChargeWindow(t, f.store, "run-classed", "build", time.Now().Add(-30*time.Second))
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := c.HeartbeatNodeClaim(claimCtx, "run-classed", "build", "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits/history", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("history = %d: %s", status, body)
	}
	var history struct {
		Charges []struct {
			Kind               string `json:"kind"`
			CPUClassCores      int64  `json:"cpu_class_cores"`
			RateMicroPerSecond int64  `json:"rate_micro_per_second"`
		} `json:"charges"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Charges) != 2 {
		t.Fatalf("charges = %d, want the reservation and the usage", len(history.Charges))
	}
	for _, charge := range history.Charges {
		// safety: a node whose plan pins no cpu resolves to one core, which the
		// smallest class covers.
		if charge.CPUClassCores != 2 || charge.RateMicroPerSecond != 10_000 {
			t.Fatalf("%s charge billed class %d at %d, want the 2-core class at 10000",
				charge.Kind, charge.CPUClassCores, charge.RateMicroPerSecond)
		}
	}

	status, body = creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /credits = %d: %s", status, body)
	}
	var state struct {
		RateTable []creditRateView `json:"rate_table"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.RateTable) != 2 || state.RateTable[0].MicroPerSecond != 10_000 {
		t.Fatalf("the credit state names %+v, want the two classes in force", state.RateTable)
	}
}

// With a table stored the scalar is the four-core entry under another name, so
// writing it alone is refused and the caller is told what to write instead.
func TestCreditSettings_RefusesTheScalarOnceATableExists(t *testing.T) {
	f := newCreditsFixture(t, false)

	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusOK {
		t.Fatalf("PUT the table = %d", status)
	}
	status, body := creditsRequest(t, http.MethodPut, f.url+"/api/v1/credits/settings", f.admin,
		map[string]any{"rate_micro_per_second": 30_000})
	if status != http.StatusBadRequest {
		t.Fatalf("PUT the scalar = %d, want 400", status)
	}
	if !strings.Contains(string(body), "rate_table") {
		t.Fatalf("the refusal does not name the table: %s", body)
	}
	_, view := creditSettings(t, f, http.MethodGet, nil)
	if view.RateMicroPerSecond != 20_000 || !view.RateTableSet {
		t.Fatalf("the refused write moved the settings: %+v", view)
	}

	// safety: naming both would let the scalar override the table's own
	// four-core entry, so the caller is told to name one.
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_micro_per_second": 21_000,
		"rate_table":            map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusBadRequest {
		t.Fatalf("PUT both = %d, want 400", status)
	}
}

// A claim for a node the table cannot price fails the node with the reason, so
// the run stops instead of every poller retrying it forever.
func TestCredits_AClaimAboveTheLargestClassFailsTheNode(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-huge", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":96}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-huge", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-huge", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-1", "lease_secs": 60})
	if status != http.StatusConflict {
		t.Fatalf("claim = %d: %s", status, body)
	}
	if !strings.Contains(string(body), controller.UnpricedCPUClassCode) {
		t.Fatalf("the refusal carries no code: %s", body)
	}
	node, err := f.store.GetNode(ctx, "run-huge", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Outcome != "failed" || node.FailureReason != store.FailureUnpricedCPUClass {
		t.Fatalf("node = %s/%s, want a failure naming the unpriced class", node.Outcome, node.FailureReason)
	}
}

// A claim that asserts its own cpu changes nothing: the class comes from the
// plan the controller holds.
func TestCredits_AClaimsOwnCPUFigureDoesNotLowerTheBill(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000, "16": 70_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":16}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	// safety: the claim surface carries no cpu figure at all, so a body that
	// asserts one is refused rather than quietly ignored.
	status, _ := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{
			"holder_id": "pod-1", "lease_secs": 60,
			"capacity": map[string]any{"max_concurrent": 1, "active_claims": 0, "cores": 0.01},
		})
	if status != http.StatusBadRequest {
		t.Fatalf("a claim asserting its own cpu = %d, want 400", status)
	}

	// safety: a sixteen-core node is above the warm class, so it is awarded by
	// the targeted claim its own Kubernetes node makes.
	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "pod-1", "lease_secs": 60})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 16 || charges[0].RateMicroPerSecond != 70_000 {
		t.Fatalf("reservation = %+v, want the 16-core class the plan asked for", charges)
	}
}

// A node above the warm class is not in the queue a warm runner polls, so it
// waits for a Kubernetes node sized to its class.
func TestCredits_AWarmClaimPassesOverAClassAboveTheWarmPool(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "root"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":8}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-1", "lease_secs": 60})
	if status != http.StatusNoContent {
		t.Fatalf("warm claim = %d: %s", status, body)
	}
	node, err := f.store.GetNode(ctx, "run-big", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Claimed || node.Outcome != "" {
		t.Fatalf("node = claimed %t outcome %q, want it still queued", node.Claimed, node.Outcome)
	}

	status, body = creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60})
	if status != http.StatusOK {
		t.Fatalf("targeted claim = %d: %s", status, body)
	}
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 8 {
		t.Fatalf("reservation = %+v, want the 8-core class", charges)
	}
}
