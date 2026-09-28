package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var claimTokenClasses = []store.ClaimRouteClass{store.ClaimSensitive, store.ClaimReporting, store.ClaimResult}

type claimedFixture struct {
	s        *store.Store
	identity store.ClaimIdentity
	node     *store.Node
}

func claimForToken(t *testing.T, runID string) claimedFixture {
	t.Helper()
	s := storetest.Open(t)
	identity := store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"}
	readyNode(t, s, runID, "build")
	node, err := s.ClaimNextReadyNode(context.Background(), identity, "holder-1", time.Minute, nil)
	if err != nil || node == nil {
		t.Fatalf("claim: %v %v", node, err)
	}
	return claimedFixture{s: s, identity: identity, node: node}
}

func (f claimedFixture) mint(t *testing.T, now time.Time) string {
	t.Helper()
	raw, err := f.s.MintClaimToken(context.Background(), store.DefaultTeam, f.node.RunID, f.node.NodeID,
		f.node.ClaimGeneration, store.ClaimTokenWork, now.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return raw
}

func wantClaimAuth(t *testing.T, s *store.Store, raw string, class store.ClaimRouteClass, now time.Time, want error) store.ClaimToken {
	t.Helper()
	tok, err := s.AuthorizeClaimToken(context.Background(), raw, class, now)
	if !errors.Is(err, want) {
		t.Fatalf("class %d: err = %v, want %v", class, err, want)
	}
	return tok
}

func TestClaimToken_LiveClaimAuthorizesEveryClassBoundToItsClaim(t *testing.T) {
	f := claimForToken(t, "run-live")
	now := time.Now()
	raw := f.mint(t, now)
	for _, class := range claimTokenClasses {
		tok := wantClaimAuth(t, f.s, raw, class, now, nil)
		if tok.Team != store.DefaultTeam || tok.RunID != "run-live" || tok.NodeID != "build" ||
			tok.Generation != f.node.ClaimGeneration || tok.Kind != store.ClaimTokenWork || tok.Ended {
			t.Fatalf("class %d resolved %+v", class, tok)
		}
	}
}

func TestClaimToken_MintRefusesAClaimItDoesNotName(t *testing.T) {
	f := claimForToken(t, "run-mint")
	ctx := context.Background()
	now := time.Now()
	cases := map[string]struct {
		team       store.Team
		generation int64
		expires    time.Time
	}{
		"later generation": {store.DefaultTeam, f.node.ClaimGeneration + 1, now.Add(time.Hour)},
		"other team":       {"other", f.node.ClaimGeneration, now.Add(time.Hour)},
		"past expiry":      {store.DefaultTeam, f.node.ClaimGeneration, now},
		"beyond 6h":        {store.DefaultTeam, f.node.ClaimGeneration, now.Add(7 * time.Hour)},
	}
	for name, c := range cases {
		if _, err := f.s.MintClaimToken(ctx, c.team, "run-mint", "build", c.generation,
			store.ClaimTokenWork, c.expires, now); err == nil {
			t.Errorf("%s: minted", name)
		}
	}
	f.mint(t, now)
	if _, err := f.s.MintClaimToken(ctx, store.DefaultTeam, "run-mint", "build", f.node.ClaimGeneration,
		store.ClaimTokenWork, now.Add(time.Hour), now); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("second token for one claim: err = %v, want ErrLockHeld", err)
	}
}

func TestClaimToken_UnknownTamperedOrExpiredTokenIsInvalid(t *testing.T) {
	f := claimForToken(t, "run-invalid")
	now := time.Now()
	raw := f.mint(t, now)
	tampered := raw[:len(raw)-1] + "A"
	if tampered == raw {
		tampered = raw[:len(raw)-1] + "B"
	}
	for _, class := range claimTokenClasses {
		wantClaimAuth(t, f.s, tampered, class, now, store.ErrClaimTokenInvalid)
		wantClaimAuth(t, f.s, "swr_"+raw[4:], class, now, store.ErrClaimTokenInvalid)
		wantClaimAuth(t, f.s, raw, class, now.Add(time.Hour), store.ErrClaimTokenInvalid)
	}
}

// The lease is the token's renewal: past the lease the claim has ended, and a
// heartbeat that extends the lease authorizes the same token again.
func TestClaimToken_HeartbeatRenewsItUntilTheLeaseLapses(t *testing.T) {
	f := claimForToken(t, "run-renew")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	later := now.Add(5 * time.Minute)
	wantClaimAuth(t, f.s, raw, store.ClaimReporting, later, store.ErrClaimNotLive)
	fenced := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: f.identity, HolderID: f.node.ClaimedBy, MembershipID: f.node.ClaimMembershipID,
		ReservationID: f.node.ReservationID, ClaimGeneration: f.node.ClaimGeneration,
	})
	if err := f.s.HeartbeatNodeClaim(fenced, "run-renew", "build", f.identity, "holder-1", 10*time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	wantClaimAuth(t, f.s, raw, store.ClaimReporting, later, nil)
	wantClaimAuth(t, f.s, raw, store.ClaimSensitive, later, nil)
}

func TestClaimToken_CancelRefusesSensitiveButNotReportingOrResult(t *testing.T) {
	f := claimForToken(t, "run-cancel")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	if err := f.s.CreateTrigger(ctx, store.Trigger{ID: "run-cancel", Pipeline: "demo", Status: "claimed", CreatedAt: now}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, nil)
	if err := f.s.RequestCancel(ctx, "run-cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, store.ErrClaimCancelRequested)
	wantClaimAuth(t, f.s, raw, store.ClaimReporting, now, nil)
	if tok := wantClaimAuth(t, f.s, raw, store.ClaimResult, now, nil); tok.Ended {
		t.Fatal("a cancel request ended the claim")
	}
}

func TestClaimToken_FinishOrLossEndsItOnTheNextRequest(t *testing.T) {
	cases := map[string]func(t *testing.T, f claimedFixture){
		"finish": func(t *testing.T, f claimedFixture) {
			if err := f.s.FinishNode(context.Background(), f.node.RunID, "build", "success", "", nil); err != nil {
				t.Fatalf("finish: %v", err)
			}
		},
		"lease reaped": func(t *testing.T, f claimedFixture) { expireAndReap(t, f) },
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			f := claimForToken(t, "run-end")
			now := time.Now()
			raw := f.mint(t, now)
			wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, nil)
			end(t, f)
			wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, store.ErrClaimNotLive)
			wantClaimAuth(t, f.s, raw, store.ClaimReporting, now, store.ErrClaimNotLive)
			if tok := wantClaimAuth(t, f.s, raw, store.ClaimResult, now, nil); !tok.Ended {
				t.Fatal("result class did not report the claim ended")
			}
		})
	}
}

func expireAndReap(t *testing.T, f claimedFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), f.node.RunID, f.node.NodeID); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, err := f.s.ReapExpiredNodeClaims(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
}

func TestClaimToken_ALaterGenerationFencesTheEarlierToken(t *testing.T) {
	f := claimForToken(t, "run-fence")
	ctx := context.Background()
	now := time.Now()
	first := f.mint(t, now)
	expireAndReap(t, f)
	next, err := f.s.ClaimNextReadyNode(ctx, f.identity, "holder-2", time.Minute, nil)
	if err != nil || next == nil || next.ClaimGeneration <= f.node.ClaimGeneration {
		t.Fatalf("reclaim: %+v %v", next, err)
	}
	second := claimedFixture{s: f.s, identity: f.identity, node: next}.mint(t, now)
	wantClaimAuth(t, f.s, first, store.ClaimSensitive, now, store.ErrClaimNotLive)
	wantClaimAuth(t, f.s, first, store.ClaimReporting, now, store.ErrClaimNotLive)
	wantClaimAuth(t, f.s, second, store.ClaimSensitive, now, nil)
}

// An automatic retry puts the claim generation back to zero, so the reset
// attempt's token must not come back to life when the retry's claim reaches
// the same generation.
func TestClaimToken_AutoRetryResetRetiresTheAttemptsToken(t *testing.T) {
	f := claimForToken(t, "run-retry")
	ctx := context.Background()
	now := time.Now()
	old := f.mint(t, now)
	if err := f.s.FinishNodeWithReason(ctx, "run-retry", "build", "failed", "boom", nil, store.FailureUnknown, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := f.s.ResetNodeForAutoRetry(ctx, "run-retry", "build"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := f.s.MarkNodeReady(ctx, "run-retry", "build"); err != nil {
		t.Fatalf("ready: %v", err)
	}
	retry, err := f.s.ClaimNextReadyNode(ctx, f.identity, "holder-2", time.Minute, nil)
	if err != nil || retry == nil || retry.ClaimGeneration != f.node.ClaimGeneration {
		t.Fatalf("retry claim: %+v %v", retry, err)
	}
	for _, class := range claimTokenClasses {
		wantClaimAuth(t, f.s, old, class, now, store.ErrClaimTokenInvalid)
	}
	claimedFixture{s: f.s, identity: f.identity, node: retry}.mint(t, now)
}

func TestClaimToken_ResultCommitsOnceAndReplaysOnlyTheSameResult(t *testing.T) {
	f := claimForToken(t, "run-result")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	tok := wantClaimAuth(t, f.s, raw, store.ClaimResult, now, nil)

	if replayed, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(tok, "hash-a"), now); err != nil || replayed {
		t.Fatalf("first commit: replayed=%v err=%v", replayed, err)
	}
	if replayed, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(tok, "hash-a"), now); err != nil || !replayed {
		t.Fatalf("live replay: replayed=%v err=%v", replayed, err)
	}
	if _, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(tok, "hash-b"), now); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("differing result while live: err = %v", err)
	}

	if err := f.s.FinishNode(ctx, "run-result", "build", "success", "", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	ended := wantClaimAuth(t, f.s, raw, store.ClaimResult, now, nil)
	if !ended.Ended {
		t.Fatal("claim not reported ended after finish")
	}
	if replayed, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(ended, "hash-a"), now); err != nil || !replayed {
		t.Fatalf("replay after the claim ended: replayed=%v err=%v", replayed, err)
	}
	if _, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(ended, "hash-b"), now); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("differing result after the claim ended: err = %v", err)
	}
	if replayed, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(ended, "hash-a"), now); err != nil || !replayed {
		t.Fatalf("the refused result overwrote the committed one: replayed=%v err=%v", replayed, err)
	}
	wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, store.ErrClaimNotLive)
}

func TestClaimToken_EndedClaimWithNoResultRefusesEveryResult(t *testing.T) {
	f := claimForToken(t, "run-noresult")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	expireAndReap(t, f)
	tok := wantClaimAuth(t, f.s, raw, store.ClaimResult, now, nil)
	if _, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(tok, "hash-a"), now); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("result after the claim was lost: err = %v", err)
	}
	var digest string
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s,
		`SELECT result_digest FROM claim_tokens WHERE run_id = ?`), "run-noresult").Scan(&digest); err != nil || digest != "" {
		t.Fatalf("a refused result wrote %q (%v)", digest, err)
	}
}

// A run no trigger names has no cancel request to read, so its own finished
// state is what refuses the sensitive class.
func TestClaimToken_TriggerlessRunRefusesSensitiveOnceItsRunEnds(t *testing.T) {
	f := claimForToken(t, "run-bare")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, nil)
	if err := f.s.FinishRun(ctx, "run-bare", "cancelled", "stopped"); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, store.ErrClaimCancelRequested)
	wantClaimAuth(t, f.s, raw, store.ClaimReporting, now, nil)
}

func TestClaimToken_SensitiveWriteFenceRechecksInsideTheTransaction(t *testing.T) {
	f := claimForToken(t, "run-fenced")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	tok := wantClaimAuth(t, f.s, raw, store.ClaimSensitive, now, nil)
	if err := store.AssertClaimSensitiveForTest(ctx, f.s, tok, now); err != nil {
		t.Fatalf("live fence: %v", err)
	}
	if err := f.s.CreateTrigger(ctx, store.Trigger{ID: "run-fenced", Pipeline: "demo", Status: "claimed", CreatedAt: now}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if err := f.s.RequestCancel(ctx, "run-fenced"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := store.AssertClaimSensitiveForTest(ctx, f.s, tok, now); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("fence after cancel: err = %v", err)
	}

	lost := claimForToken(t, "run-fence-lost")
	lostTok := wantClaimAuth(t, lost.s, lost.mint(t, now), store.ClaimSensitive, now, nil)
	expireAndReap(t, lost)
	if err := store.AssertClaimSensitiveForTest(ctx, lost.s, lostTok, now); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("fence after the lease lapsed: err = %v", err)
	}
}

func TestClaimToken_ReplayAnswersOnlyTheCommittedDigest(t *testing.T) {
	f := claimForToken(t, "run-replay")
	ctx := context.Background()
	now := time.Now()
	raw := f.mint(t, now)
	tok := wantClaimAuth(t, f.s, raw, store.ClaimResult, now, nil)
	if err := f.s.ReplayClaimResult(ctx, tok, "hash-a"); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("replay before any commit: err = %v", err)
	}
	if _, err := store.CommitClaimResultForTest(ctx, f.s, store.NewClaimResultCommit(tok, "hash-a"), now); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := f.s.ReplayClaimResult(ctx, tok, "hash-a"); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if err := f.s.ReplayClaimResult(ctx, tok, "hash-b"); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("differing replay: err = %v", err)
	}
}
