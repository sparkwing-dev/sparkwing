package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

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
