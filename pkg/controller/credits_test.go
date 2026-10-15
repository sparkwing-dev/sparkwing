package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	return newCreditsFixtureWithLicense(t, metered, license.FeatureMetering)
}

func newCreditsFixtureWithLicense(t *testing.T, metered bool, feature string) creditsFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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
		[]string{controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState, controller.ScopeRunsRead},
		0, now, store.TokenOptions{Metered: metered})
	if err != nil {
		t.Fatalf("runner token: %v", err)
	}
	readonly, _, err := st.CreateToken("viewer", store.TokenKindUser,
		[]string{controller.ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("reader token: %v", err)
	}
	ctrl := controller.New(st, nil).EnableAuthFromStore()
	controller.ConfigureConsoleFixture(ctrl)
	if feature != "" {
		pub, priv := licensetest.NewKey(t)
		raw := licensetest.Sign(t, priv, licensetest.Terms{
			Features: []string{feature}, IssuedTo: "test",
			IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
		})
		ctrl.WithLicense(license.Resolve(raw, pub, time.Now(), nil))
	}
	srv := httptest.NewServer(ctrl.Handler())
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
		`UPDATE nodes SET credit_charged_through = ?,
		 execution_started_at = COALESCE(execution_started_at, ?)
		 WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), at.Add(-store.MinBillableSeconds*time.Second).UnixNano(), runID, nodeID); err != nil {
		t.Fatalf("rewind the charge window: %v", err)
	}
}

func ageExhaustionStamp(t *testing.T, st *store.Store, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE teams SET credit_exhausted_at = ? WHERE name = ?`,
		at.UnixNano(), string(store.DefaultTeam)); err != nil {
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
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
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

// A metered pool's trigger claim starts a run, so an empty balance refuses it
// with the same 402 a node claim gets, and the run records why it waits.
func TestCredits_MeteredTriggerClaimRefusedOnAnEmptyBalance(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, f.runner).WithTriggerNodeRunner("k8s")
	now := time.Now()
	if err := f.store.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "run-broke", Pipeline: "build", Status: "pending", CreatedAt: now,
	}, store.Run{ID: "run-broke", Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimTrigger(ctx); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("trigger claim error = %v, want ErrInsufficientCredits", err)
	}
	events, err := f.store.ListEventsAfter(ctx, "run-broke", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	blocked := 0
	for _, e := range events {
		if e.Kind == store.EventKindCreditsBlocked {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("credits_blocked events = %d, want 1", blocked)
	}
}

func TestCredits_TriggerHeartbeatLedgerFailureRefusesRenewal(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.store.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "run-ledger-stop", Pipeline: "build", Status: "pending", CreatedAt: now,
	}, store.Run{ID: "run-ledger-stop", Pipeline: "build", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c := client.NewWithToken(f.url, nil, f.runner).WithTriggerNodeRunner("k8s")
	claimed, err := c.ClaimTrigger(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err := f.store.DB().Exec(`UPDATE triggers SET credit_reserved_at = ? WHERE id = ?`,
		time.Now().Add(-(store.MinBillableSeconds+5)*time.Second).UnixNano(), claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(`CREATE TRIGGER reject_trigger_usage BEFORE INSERT ON credit_charges
		WHEN NEW.kind = 'usage' AND NEW.run_id = 'run-ledger-stop'
		BEGIN SELECT RAISE(FAIL, 'ledger write failed'); END`); err != nil {
		t.Fatal(err)
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})
	if _, err := c.HeartbeatTrigger(fenced, claimed.ID); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("failed ledger heartbeat = %v, want the terminal 409 claim refusal", err)
	}
	got, err := f.store.GetTrigger(ctx, claimed.ID)
	if err != nil || got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) {
		t.Fatalf("ledger failure renewed lease from %v to %+v, %v", claimed.LeaseExpiresAt, got, err)
	}
}

func TestCredits_UnsettledTriggerClaimIsActionableWithoutBlockingQueue(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	c := client.NewWithToken(f.url, nil, f.runner).WithTriggerNodeRunner("k8s")
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: "run-unsettled", Pipeline: "build", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	prior, err := c.ClaimTrigger(ctx)
	if err != nil || prior == nil {
		t.Fatalf("prior claim = %+v, %v", prior, err)
	}
	if ok, err := f.store.ReleaseClaimAtGeneration(store.WithoutCreditMetering(ctx), prior.ID, prior.ClaimSeq); err != nil || !ok {
		t.Fatalf("release without settlement = %t, %v", ok, err)
	}
	if _, err := c.ClaimSpecificTrigger(ctx, prior.ID, time.Minute); !errors.Is(err, store.ErrLockHeld) ||
		!strings.Contains(err.Error(), "unsettled prior metered claim") {
		t.Fatalf("named claim = %v, want actionable conflict", err)
	}
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: "run-ready", Pipeline: "build", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if next, err := c.ClaimTrigger(ctx); err != nil || next == nil || next.ID != "run-ready" {
		t.Fatalf("next claim behind parked trigger = %+v, %v", next, err)
	}
}

// A metered pool that ran a claimed trigger's nodes in its own process would
// run them outside any node claim, and so outside any credit charge. A
// metered trigger claim must name a node runner that claims each node.
func TestCredits_MeteredTriggerClaimRefusesInProcessNodes(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		if err := f.store.CreateTriggerWithRun(ctx, store.Trigger{
			ID: id, Pipeline: "build", Status: "pending", CreatedAt: now,
		}, store.Run{ID: id, Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]*client.Client{
		"no node runner named": client.NewWithToken(f.url, nil, f.runner),
		"inprocess":            client.NewWithToken(f.url, nil, f.runner).WithTriggerNodeRunner("inprocess"),
		"removed warm":         client.NewWithToken(f.url, nil, f.runner).WithTriggerNodeRunner("warm"),
	} {
		if tr, err := c.ClaimTrigger(ctx); !errors.Is(err, store.ErrMeteredInProcessNodes) {
			t.Errorf("%s: trigger claim = %+v, %v; want ErrMeteredInProcessNodes", name, tr, err)
		}
		if tr, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); !errors.Is(err, store.ErrMeteredInProcessNodes) {
			t.Errorf("%s: named trigger claim = %+v, %v; want ErrMeteredInProcessNodes", name, tr, err)
		}
	}
	if tr, err := client.NewWithToken(f.url, nil, f.runner).WithTriggerNodeRunner("k8s").ClaimTrigger(ctx); err != nil || tr == nil {
		t.Fatalf("metered claim naming the k8s node runner = %+v, %v; want a trigger", tr, err)
	}
}

func TestCredits_MeteredClaimChargesEachHeartbeat(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
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

	// safety: moving the execution charge window makes the next heartbeat cover
	// a known interval without waiting on the clock.
	setNodeChargeWindow(t, f.store, "run-paid", "build", time.Now().Add(-30*time.Second))

	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := c.HeartbeatNodeClaim(claimCtx, "run-paid", "build", "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
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

func TestCredits_MeteredHeartbeatRollsBackWhenLedgerFails(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatal(err)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-ledger-fault", "build")
	if err := f.store.MarkNodeReady(ctx, "run-ledger-fault", "build"); err != nil {
		t.Fatal(err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	setNodeChargeWindow(t, f.store, n.RunID, n.NodeID, time.Now().Add(-30*time.Second))
	before, err := f.store.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var chargedThroughBefore int64
	if err := f.store.DB().QueryRow(`SELECT credit_charged_through FROM nodes WHERE run_id = ? AND node_id = ?`, n.RunID, n.NodeID).Scan(&chargedThroughBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(`CREATE TRIGGER fail_node_usage BEFORE INSERT ON credit_charges
		WHEN NEW.kind = 'usage' BEGIN SELECT RAISE(ABORT, 'ledger unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost,
		f.url+"/api/v1/runs/"+n.RunID+"/nodes/"+n.NodeID+"/heartbeat",
		strings.NewReader(`{"holder_id":"pod-1","lease_secs":300}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.runner)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.ClaimHolderHeader, n.ClaimedBy)
	req.Header.Set(store.ClaimMembershipHeader, n.ClaimMembershipID)
	req.Header.Set(store.ClaimReservationHeader, n.ReservationID)
	req.Header.Set(store.ClaimGenerationHeader, fmt.Sprint(n.ClaimGeneration))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("heartbeat status = %d, want 409", resp.StatusCode)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(responseBody), "ledger unavailable") || !strings.Contains(string(responseBody), store.ErrLockHeld.Error()) {
		t.Fatalf("heartbeat exposed the ledger error: %s", responseBody)
	}
	after, err := f.store.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) || after.ClaimedBy != before.ClaimedBy ||
		after.ClaimGeneration != before.ClaimGeneration {
		t.Fatalf("ledger failure changed claim or charge window: before=%+v after=%+v", before, after)
	}
	var chargedThroughAfter int64
	if err := f.store.DB().QueryRow(`SELECT credit_charged_through FROM nodes WHERE run_id = ? AND node_id = ?`, n.RunID, n.NodeID).Scan(&chargedThroughAfter); err != nil {
		t.Fatal(err)
	}
	if chargedThroughAfter != chargedThroughBefore {
		t.Fatalf("ledger failure advanced charge window from %d to %d", chargedThroughBefore, chargedThroughAfter)
	}
	if _, err := f.store.DB().Exec(`ALTER TABLE tokens RENAME TO tokens_unavailable`); err != nil {
		t.Fatal(err)
	}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		Claimant: store.ClaimIdentity{Principal: "pool", TokenPrefix: f.prefix},
	})
	_, _, err = f.store.HeartbeatNodeClaimWithCredits(claimCtx, n.RunID, n.NodeID,
		store.ClaimIdentity{Principal: "pool", TokenPrefix: f.prefix}, n.ClaimedBy, 5*time.Minute, true)
	if err == nil {
		t.Fatal("heartbeat renewed while the token marker was unreadable")
	}
	after, err = f.store.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) || after.ClaimedBy != before.ClaimedBy {
		t.Fatalf("token lookup failure changed claim: before=%+v after=%+v", before, after)
	}
}

func TestCredits_MeteredHeartbeatDoesNotReviveClaimExpiredDuringCharge(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatal(err)
	}
	c := client.NewWithToken(f.url, nil, f.runner)
	seedRunNode(t, f.store, "run-expiring-charge", "build")
	if err := f.store.MarkNodeReady(ctx, "run-expiring-charge", "build"); err != nil {
		t.Fatal(err)
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	setNodeChargeWindow(t, f.store, n.RunID, n.NodeID, time.Now().Add(-30*time.Second))
	// safety: expiring just after the charge timestamp models a ledger wait
	// without sleeping, so a stale renewal clock would revive the claim.
	if _, err := f.store.DB().Exec(`CREATE TRIGGER expire_claim_on_usage AFTER INSERT ON credit_charges
		WHEN NEW.kind = 'usage' BEGIN UPDATE nodes SET lease_expires_at = NEW.charged_at + 1
		WHERE run_id = NEW.run_id AND node_id = NEW.node_id; END`); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "pool", TokenPrefix: f.prefix}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		Claimant: claimant,
	})
	_, _, err = f.store.HeartbeatNodeClaimWithCredits(claimCtx, n.RunID, n.NodeID,
		claimant, n.ClaimedBy, 5*time.Minute, true)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("heartbeat after claim expiry = %v, want ErrLockHeld", err)
	}
	after, err := f.store.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LeaseExpiresAt.Equal(*n.LeaseExpiresAt) || after.ClaimedBy != n.ClaimedBy {
		t.Fatalf("failed heartbeat changed the claim: before=%+v after=%+v", n, after)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if usage := creditChargesOfKind(charges, store.CreditChargeUsage); len(usage) != 0 {
		t.Fatalf("expired claim kept a usage charge: %+v", usage)
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
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	floor := unpinnedNodeRateMicro * store.MinBillableSeconds
	// safety: exactly one reservation, so the first heartbeat past it spends
	// the balance.
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: floor, Reference: "", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	grace := int64(1)
	if _, err := f.store.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
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

// A node that finishes between heartbeats and inside its reservation pays the
// minimum billable seconds, because the finish consumes the reservation.
func TestCredits_NodeFinishSettlesAShortNodeAtTheMinimum(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	billingTenant, err := f.store.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	before, err := billingTenant.CreditBalanceMicro(ctx)
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

	// safety: the claim reserved through claim plus the minimum; placing that
	// instant four seconds short of it is a node claimed four seconds ago,
	// finishing before the first heartbeat would have fired.
	setNodeChargeWindow(t, f.store, "run-brief", "build",
		time.Now().Add(time.Duration(store.MinBillableSeconds-4)*time.Second))
	if err := c.StartNode(claimCtx, "run-brief", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	if err := c.FinishNode(claimCtx, "run-brief", "build", "success", "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	after, err := billingTenant.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if spent, want := before-after, int64(store.MinBillableSeconds)*unpinnedNodeRateMicro; spent != want {
		t.Fatalf("spent %d for four seconds of work, want the minimum, %d", spent, want)
	}
}

func TestCredits_RoutesShowGrantAndHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
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

	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_9", CreatedBy: "test"}); err != nil {
		t.Fatal(err)
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
	if history.Grants[0].Reference != "pay_9" || history.Grants[0].CreatedBy != "test" {
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
func TestCredits_HistoryNamesTheClassAndRateEachChargeWasBilledAt(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
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

func TestCredits_AClaimAboveTheLargestClassFailsTheNode(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
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
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
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
		map[string]any{"holder_id": "pod-1", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 16 || charges[0].RateMicroPerSecond != 70_000 {
		t.Fatalf("reservation = %+v, want the 16-core class the plan asked for", charges)
	}
}

// The class the claim response carries is the class the charge row bills,
// whichever ladder the operator priced.
func TestCredits_TheClaimResponseCarriesTheBilledClass(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "64": 270_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":3}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	var claimed store.Node
	if err := json.Unmarshal(body, &claimed); err != nil {
		t.Fatalf("decode the claim: %v: %s", err, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 64 {
		t.Fatalf("reservation = %+v, want the 64-core class this ladder prices", charges)
	}
	if claimed.CreditCPUClassCores != charges[0].CPUClassCores {
		t.Fatalf("claim says class %d, charge row says %d",
			claimed.CreditCPUClassCores, charges[0].CPUClassCores)
	}
	if want := store.CPUClassMemoryBytes(64); claimed.CreditCPUClassMemoryBytes != want {
		t.Fatalf("claim memory = %d, want %d", claimed.CreditCPUClassMemoryBytes, want)
	}
}

func TestCredits_QuarterCorePinReservesTwoCoreMinimum(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay-quarter", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-quarter", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":0.25}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-quarter", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-quarter", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-quarter/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 2 ||
		charges[0].RateMicroPerSecond != 9_000 ||
		charges[0].AmountMicro != 9_000*store.MinBillableSeconds {
		t.Fatalf("reservation = %+v, want the minimum billable seconds at the two-core class", charges)
	}
}

// Naming a node is not a way around the ladder: a claimant outside the
// class-routed path is held to the warm class.
func TestCredits_ANamedClaimThatDoesNotSizeToClassIsRefused(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
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

	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "runner:warm", "lease_secs": 60})
	if status != http.StatusConflict {
		t.Fatalf("named claim without sizes_to_class = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("named claim that sizes to the class = %d: %s", status, body)
	}
}

// A node above the warm class is not in the queue a warm runner polls, so it
// waits for a Kubernetes node sized to its class.
func TestCredits_AWarmClaimPassesOverAClassAboveTheWarmPool(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
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
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("targeted claim = %d: %s", status, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 8 {
		t.Fatalf("reservation = %+v, want the 8-core class", charges)
	}
}
