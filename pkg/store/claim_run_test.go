package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A launch claim reserves its team's credits; a Job that never got a machine
// hands the reservation back whole, and a pod's first beat opens billing,
// after which its claim is never handed back.
func TestLaunchBilling_ReleaseRefundsAndTheFirstBeatBills(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	_, lt, err := st.CreateToken("launcher", store.TokenKindService, []string{store.LaunchScope}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	launcher := store.ClaimIdentity{Principal: lt.Principal, TokenPrefix: lt.Prefix}
	paying := teamHandle(t, st, "paying")
	if err := optInForTest(t, st, paying, "korey", "probe"); err != nil {
		t.Fatal(err)
	}
	intake(t, paying, "run-billed", "korey", "probe")
	const granted = 100 * store.MicroCreditsPerCent
	if _, err := paying.GrantCredits(ctx, store.CreditGrantPaid, granted, "pay_launch", "admin"); err != nil {
		t.Fatal(err)
	}
	claim := func(at time.Time) store.LaunchJob {
		t.Helper()
		c, err := st.ClaimLaunch(ctx, launcher, launchRequest(), at)
		if err != nil || c == nil {
			t.Fatalf("launch claim: %+v %v", c, err)
		}
		if balance, err := paying.CreditBalanceMicro(ctx); err != nil || balance >= granted {
			t.Fatalf("balance after the claim = %d (%v); nothing reserved", balance, err)
		}
		return store.LaunchJob{RunID: c.RunID, NodeID: c.NodeID, Generation: c.Generation, Release: true}
	}
	first := claim(time.Now())
	if res, err := st.SyncLaunchJobs(ctx, launcher, []store.LaunchJob{first}, time.Now()); err != nil || res[0].State != store.LaunchJobDelete {
		t.Fatalf("release = %+v %v", res, err)
	}
	if balance, err := paying.CreditBalanceMicro(ctx); err != nil || balance != granted {
		t.Fatalf("balance after the release = %d (%v), want the whole grant back", balance, err)
	}
	second := claim(time.Now().Add(store.ReleaseBackoff))
	tok := store.ClaimToken{Team: paying.Team(), RunID: second.RunID, NodeID: second.NodeID, Generation: second.Generation}
	if _, err := st.HeartbeatClaim(ctx, tok, time.Minute, time.Now()); err != nil {
		t.Fatalf("beat: %v", err)
	}
	var billingFrom int64
	if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, `SELECT credit_billing_from FROM nodes WHERE run_id = ? AND node_id = ?`),
		second.RunID, second.NodeID).Scan(&billingFrom); err != nil || billingFrom == 0 {
		t.Fatalf("billing after the first beat opened at %d (%v)", billingFrom, err)
	}
	if res, err := st.SyncLaunchJobs(ctx, launcher, []store.LaunchJob{second}, time.Now()); err != nil || res[0].State != store.LaunchJobKeep {
		t.Fatalf("releasing a billed claim = %+v %v, want keep", res, err)
	}
}

// A launch claim only reserves: the Job's wait for a machine is never billed,
// the pod's first renewal opens the charge window, and the attempt pays the
// minimum from that renewal plus the seconds past it.
func TestClaimLaunch_ReservesAtTheClaimAndBillsFromTheFirstRenewal(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	_, lt, err := st.CreateToken("launcher", store.TokenKindService, []string{store.LaunchScope}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	launcher := store.ClaimIdentity{Principal: lt.Principal, TokenPrefix: lt.Prefix}
	team := teamHandle(t, st, "paying")
	if err := optInForTest(t, st, team, "korey", "probe"); err != nil {
		t.Fatal(err)
	}
	intake(t, team, "run-launch-bill", "korey", "probe")
	granted := int64(100 * store.MicroCreditsPerCent)
	if _, err := team.GrantCredits(ctx, store.CreditGrantPaid, granted, "pay_launch_bill", "admin"); err != nil {
		t.Fatal(err)
	}
	balance := func() int64 {
		t.Helper()
		b, err := team.CreditBalanceMicro(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	claimedAt := time.Now()
	claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), claimedAt)
	if err != nil || claim == nil {
		t.Fatalf("launch claim: %+v %v", claim, err)
	}
	tok, err := st.AuthorizeClaimToken(ctx, claim.Token, store.ClaimReporting, claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	reserved := granted - balance()
	if reserved <= 0 || reserved%store.MinBillableSeconds != 0 {
		t.Fatalf("the claim reserved %d micro-credits, want the minimum at the class rate", reserved)
	}
	rate := reserved / store.MinBillableSeconds
	if from := billingFrom(t, st, claim.RunID, claim.NodeID); from != 0 {
		t.Fatalf("billing opened at %d, at the claim", from)
	}

	renewedAt := claimedAt.Add(40 * time.Second)
	if _, err := st.HeartbeatClaim(ctx, tok, store.MaxLeaseDuration, renewedAt); err != nil {
		t.Fatal(err)
	}
	if from := billingFrom(t, st, claim.RunID, claim.NodeID); from != renewedAt.UnixNano() {
		t.Fatalf("billing start = %d, want the first renewal %d", from, renewedAt.UnixNano())
	}
	if got := balance(); got != granted-reserved {
		t.Fatalf("balance after the first renewal = %d, want %d: the wait for a machine was billed", got, granted-reserved)
	}

	endedAt := renewedAt.Add((store.MinBillableSeconds + 10) * time.Second)
	result, err := st.AuthorizeClaimToken(ctx, claim.Token, store.ClaimResult, endedAt)
	if err != nil {
		t.Fatal(err)
	}
	rep := store.AttemptReport{Outcome: "failed", Error: "plan: boom"}
	body, _ := json.Marshal(rep)
	if _, err := st.ReportAttempt(ctx, store.NewClaimResultCommit(result, digestOf(body)), rep, endedAt); err != nil {
		t.Fatal(err)
	}
	if got, want := balance(), granted-reserved-10*rate; got != want {
		t.Fatalf("balance at the end = %d, want %d: the minimum and ten seconds from the renewal", got, want)
	}
}
