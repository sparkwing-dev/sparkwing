package controller_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type namedClaimFixture struct {
	url   string
	token string
	store *store.Store
}

func newNamedClaimFixture(t *testing.T, opts store.TokenOptions) namedClaimFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, _, err := st.CreateTokenWith(context.Background(), "pool", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeRunsRead},
		0, time.Now().UTC(), opts)
	if err != nil {
		t.Fatalf("CreateTokenWith: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return namedClaimFixture{url: srv.URL, token: token, store: st}
}

func TestClaimNodeByID_AwardsTheNamedNodeWithItsFence(t *testing.T) {
	f := newNamedClaimFixture(t, store.TokenOptions{})
	seedRunNode(t, f.store, "run-1", "build")
	c := client.NewWithToken(f.url, nil, f.token)
	ctx := context.Background()

	n, err := c.ClaimNodeByID(ctx, "run-1", "build", "k8s-job:sw-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNodeByID: %v", err)
	}
	if n.ClaimedBy != "k8s-job:sw-1" || n.ClaimGeneration < 1 {
		t.Fatalf("claim response = %+v, want the holder and a generation the pod can send", n)
	}

	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	fenced := store.WithNodeClaimFence(ctx, fence)
	if err := c.UpdateNodeActivity(fenced, "run-1", "build", "running in the pod"); err != nil {
		t.Fatalf("a write under the awarded fence was refused: %v", err)
	}
	if err := c.UpdateNodeActivity(ctx, "run-1", "build", "unfenced"); err == nil {
		t.Fatal("an unfenced write on a claimed node was admitted")
	}
	if err := c.HeartbeatNodeClaim(fenced, "run-1", "build", n.ClaimedBy, 5*time.Minute, nil); err != nil {
		t.Fatalf("renewing the awarded claim was refused: %v", err)
	}
}

func TestClaimNodeByID_RefusesANodeAnotherHolderHas(t *testing.T) {
	f := newNamedClaimFixture(t, store.TokenOptions{})
	seedRunNode(t, f.store, "run-1", "build")
	ctx := context.Background()
	if _, err := f.store.ClaimNamedNode(ctx, store.ClaimIdentity{},
		"run-1", "build", "agent:box-a", time.Minute); err != nil {
		t.Fatalf("seed the agent's claim: %v", err)
	}

	_, err := client.NewWithToken(f.url, nil, f.token).
		ClaimNodeByID(ctx, "run-1", "build", "k8s-job:sw-1", time.Minute)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("claiming a held node = %v, want ErrLockHeld", err)
	}
}

func TestClaimNodeByID_ReportsANodeThatDoesNotExist(t *testing.T) {
	f := newNamedClaimFixture(t, store.TokenOptions{})
	_, err := client.NewWithToken(f.url, nil, f.token).
		ClaimNodeByID(context.Background(), "run-missing", "build", "k8s-job:sw-1", time.Minute)
	if err == nil {
		t.Fatal("claiming an absent node succeeded")
	}
}

func TestClaimNodeByID_NeedsTheClaimScope(t *testing.T) {
	f := newNamedClaimFixture(t, store.TokenOptions{})
	seedRunNode(t, f.store, "run-1", "build")
	reader, _, err := f.store.CreateToken("reader", store.TokenKindRunner,
		[]string{controller.ScopeRunsRead}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if _, err := client.NewWithToken(f.url, nil, reader).
		ClaimNodeByID(context.Background(), "run-1", "build", "k8s-job:sw-1", time.Minute); err == nil {
		t.Fatal("a token without nodes.claim claimed a node")
	}
}

// The cloud pool bills the minute through the token that holds the claim, so a
// node the dispatcher claims for a Job must reserve on the claim and settle on
// the finish.
func TestClaimNodeByID_MeteredTokenReservesAndSettles(t *testing.T) {
	f := newNamedClaimFixture(t, store.TokenOptions{Metered: true})
	seedRunNode(t, f.store, "run-1", "build")
	ctx := context.Background()
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("GrantCredits: %v", err)
	}
	c := client.NewWithToken(f.url, nil, f.token)

	n, err := c.ClaimNodeByID(ctx, "run-1", "build", "k8s-job:sw-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNodeByID: %v", err)
	}
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("ListCreditCharges: %v", err)
	}
	if len(charges) != 1 || charges[0].Kind != store.CreditChargeReservation {
		t.Fatalf("charges after the claim = %+v, want one reservation", charges)
	}

	rewindNamedChargeWindow(t, f.store, "run-1", "build", time.Now().Add(-90*time.Second))
	fenced := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := c.FinishNode(fenced, "run-1", "build", "success", "", nil); err != nil {
		t.Fatalf("FinishNode under the claim fence: %v", err)
	}
	charges, err = f.store.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("ListCreditCharges: %v", err)
	}
	if len(charges) != 2 {
		t.Fatalf("charges after the finish = %+v, want the reservation and the settlement", charges)
	}
}

func rewindNamedChargeWindow(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), runID, nodeID); err != nil {
		t.Fatalf("rewind the charge window: %v", err)
	}
}
