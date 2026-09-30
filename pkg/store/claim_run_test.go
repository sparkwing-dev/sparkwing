package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func (f dispatchRun) launch(t *testing.T, at time.Time) (*store.LaunchClaim, store.ClaimToken) {
	t.Helper()
	claim, err := f.s.ClaimLaunch(context.Background(), launcherIdentity, launchRequest(), at)
	if err != nil || claim == nil {
		t.Fatalf("launch claim: %+v %v", claim, err)
	}
	tok, err := f.s.AuthorizeClaimToken(context.Background(), claim.Token, store.ClaimReporting, at)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return claim, tok
}

func capacityWaits(t *testing.T, s *store.Store, runID string) int {
	t.Helper()
	events, err := s.ListEventsAfter(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == "capacity_wait" {
			n++
		}
	}
	return n
}

// The execution start is the line after which a claim runs pipeline code: it
// is stamped once, closes the source mint, hands a work node its planned
// hash, and is refused once the run is being cancelled.
func TestStartClaimExecution_StampsOnceAndClosesTheSourceMint(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-exec")
	_, tok := f.launch(t, time.Now())
	hash, err := f.s.StartClaimExecution(ctx, tok, time.Now())
	if err != nil || hash != "" {
		t.Fatalf("plan node start = %q, %v", hash, err)
	}
	first := f.node(t, store.PlanNodeID).ExecutionStartedAt
	if first == nil {
		t.Fatal("the planning node has no execution start")
	}
	if _, err := f.s.StartClaimExecution(ctx, tok, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if again := f.node(t, store.PlanNodeID).ExecutionStartedAt; again == nil || !again.Equal(*first) {
		t.Fatalf("a repeated start moved the stamp: %v -> %v", first, again)
	}
	team, err := f.s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.SpendSourceCredential(ctx, tok, "cred", "github.com", "pod", time.Now()); !errors.Is(err, store.ErrSourceCredentialSpent) {
		t.Fatalf("a source mint after the start: err = %v, want ErrSourceCredentialSpent", err)
	}
	if _, err := f.accept(tok, planOf("a")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	_, work := f.launch(t, time.Now())
	if hash, err := f.s.StartClaimExecution(ctx, work, time.Now()); err != nil || hash != hashA {
		t.Fatalf("work node start = %q, %v; want the accepted plan's hash", hash, err)
	}

	g := newDispatchRunOn(t, f.s, "run-exec-cancel")
	_, cancelled := g.launch(t, time.Now())
	if err := f.s.RequestCancel(ctx, "run-exec-cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.StartClaimExecution(ctx, cancelled, time.Now()); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("start after cancel: err = %v, want ErrClaimCancelRequested", err)
	}
	if n := g.node(t, store.PlanNodeID); n.ExecutionStartedAt != nil {
		t.Fatalf("a cancelled claim was stamped as started at %v", n.ExecutionStartedAt)
	}
}

// A beat renews the claim and carries a cancel request without ending the
// claim, so the pod can still report; a lost claim is refused.
func TestHeartbeatClaim_RenewsCarriesCancelAndRefusesALostClaim(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-beat")
	_, tok := f.launch(t, time.Now())
	beatAt := time.Now()
	beat, err := f.s.HeartbeatClaim(ctx, tok, 2*time.Minute, beatAt)
	if err != nil || beat.Cancel {
		t.Fatalf("beat = %+v, %v", beat, err)
	}
	if n := f.node(t, store.PlanNodeID); n.LeaseExpiresAt == nil || n.LeaseExpiresAt.Before(beatAt.Add(2*time.Minute)) {
		t.Fatalf("lease after the beat = %v, want two minutes past %v", n.LeaseExpiresAt, beatAt)
	}
	if err := f.s.RequestCancel(ctx, "run-beat"); err != nil {
		t.Fatal(err)
	}
	if beat, err := f.s.HeartbeatClaim(ctx, tok, 2*time.Minute, time.Now()); err != nil || !beat.Cancel {
		t.Fatalf("beat after cancel = %+v, %v; want renewed with the cancel", beat, err)
	}
	if _, err := f.s.HeartbeatClaim(ctx, tok, time.Minute, time.Now().Add(5*time.Minute)); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("beat after the lease lapsed: err = %v, want ErrLockHeld", err)
	}
	stale := tok
	stale.Generation++
	if _, err := f.s.HeartbeatClaim(ctx, stale, time.Minute, time.Now()); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("beat for another generation: err = %v, want ErrLockHeld", err)
	}
}

// A Job that never got a machine hands its claim back: the node waits in the
// queue with no attempt spent and is claimed again later. A claim whose pod
// started is never taken back, and an ended or cancelled claim's Job goes.
func TestSyncLaunchJobs_ReleasesOnlyAnUnstartedClaim(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-sync")
	claim, _ := f.launch(t, time.Now())
	job := store.LaunchJob{RunID: claim.RunID, NodeID: claim.NodeID, Generation: claim.Generation}
	if got := syncOne(t, f.s, job); got != store.LaunchJobKeep {
		t.Fatalf("a live claim's Job: %s, want keep", got)
	}
	if got := syncOne(t, f.s, store.LaunchJob{RunID: job.RunID, NodeID: job.NodeID, Generation: job.Generation, Release: true}); got != store.LaunchJobDelete {
		t.Fatalf("releasing an unstarted claim: %s, want delete", got)
	}
	n := f.node(t, store.PlanNodeID)
	if n.ClaimedBy != "" || n.AttemptsConsumed != 0 || n.StatusDetail != store.CapacityWaitDetail {
		t.Fatalf("released node: claimed_by %v, attempts %d, detail %q", n.ClaimedBy, n.AttemptsConsumed, n.StatusDetail)
	}
	if _, err := f.s.AuthorizeClaimToken(ctx, claim.Token, store.ClaimReporting, time.Now()); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("the released claim's token: err = %v, want ErrClaimNotLive", err)
	}
	if capacityWaits(t, f.s, "run-sync") != 1 {
		t.Fatal("the release recorded no capacity_wait event")
	}

	if c, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now()); err != nil || c != nil {
		t.Fatalf("the released node was claimed inside its backoff: %+v %v", c, err)
	}
	again, retok := f.launch(t, time.Now().Add(store.ReleaseBackoff))
	if again.Generation <= claim.Generation || f.node(t, store.PlanNodeID).StatusDetail != "" {
		t.Fatalf("reclaimed at generation %d after %d, detail %q", again.Generation, claim.Generation, f.node(t, store.PlanNodeID).StatusDetail)
	}
	if _, err := f.s.StartClaimExecution(ctx, retok, time.Now()); err != nil {
		t.Fatal(err)
	}
	current := store.LaunchJob{RunID: again.RunID, NodeID: again.NodeID, Generation: again.Generation, Release: true}
	if got := syncOne(t, f.s, current); got != store.LaunchJobKeep {
		t.Fatalf("releasing a started claim: %s, want keep", got)
	}
	if got := syncOne(t, f.s, job); got != store.LaunchJobDelete {
		t.Fatalf("the superseded claim's Job: %s, want delete", got)
	}
	other := launcherIdentity
	other.TokenPrefix = "swr_other"
	if res, err := f.s.SyncLaunchJobs(ctx, other, []store.LaunchJob{current}, time.Now()); err != nil || res[0].State != store.LaunchJobDelete {
		t.Fatalf("another launcher's view of the claim: %+v %v, want delete", res, err)
	}
	if err := f.s.RequestCancel(ctx, "run-sync"); err != nil {
		t.Fatal(err)
	}
	current.Release = false
	if got := syncOne(t, f.s, current); got != store.LaunchJobDelete {
		t.Fatalf("a cancelled run's Job: %s, want delete", got)
	}
}

func syncOne(t *testing.T, s *store.Store, job store.LaunchJob) store.LaunchJobState {
	t.Helper()
	res, err := s.SyncLaunchJobs(context.Background(), launcherIdentity, []store.LaunchJob{job}, time.Now())
	if err != nil || len(res) != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	return res[0].State
}

// A child of an opted-in repository is planned by the controller, never
// claimed as a trigger; a child of any other repository keeps the trigger
// path. Either way only its parent run reads it as its child.
func TestEnqueueChildRun_RoutesAChildByItsRepository(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	if err := optInForTest(t, st, alpha, "korey", "probe"); err != nil {
		t.Fatal(err)
	}
	intake(t, alpha, "run-parent", "korey", "probe")
	f := dispatchRun{s: st, run: "run-parent", claimant: launcherIdentity}
	_, planTok := f.launch(t, time.Now())
	if _, err := f.accept(planTok, planOf(`a|"modifiers":{"retry":1,"retry_auto":true}`)); err != nil {
		t.Fatal(err)
	}
	_, tok := f.launch(t, time.Now())
	child := func(ordinal int64, id, repo string) {
		t.Helper()
		if got, err := st.EnqueueChildRun(ctx, tok, ordinal, store.Trigger{
			ID: id, Pipeline: "sub", GithubOwner: "korey", GithubRepo: repo, Repo: "korey/" + repo,
		}, time.Now()); err != nil || got != id {
			t.Fatalf("enqueue %s: %q %v", id, got, err)
		}
	}
	child(0, "child-probe", "probe")
	child(1, "child-other", "other")
	if _, err := st.GetNode(ctx, "child-probe", store.PlanNodeID); err != nil {
		t.Fatalf("the opted-in child has no planning node: %v", err)
	}
	requireTriggerStatus(t, st, "child-probe", "done")
	if _, err := st.GetNode(ctx, "child-other", store.PlanNodeID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the opted-out child got a planning node: %v", err)
	}
	requireTriggerStatus(t, st, "child-other", "pending")
	if run, err := st.GetRun(ctx, "child-other"); err != nil || run.ParentRunID != "run-parent" {
		t.Fatalf("the opted-out child's run = %+v, %v", run, err)
	}
	for id, want := range map[string]bool{"child-probe": true, "run-parent": false} {
		if got, err := st.IsChildRunOf(ctx, alpha.Team(), "run-parent", tok.NodeID, id); err != nil || got != want {
			t.Fatalf("IsChildRunOf(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
	if got, err := st.IsChildRunOf(ctx, store.DefaultTeam, "run-parent", tok.NodeID, "child-probe"); err != nil || got {
		t.Fatalf("another team reads the child as run-parent's: %v %v", got, err)
	}
	if got, err := st.IsChildRunOf(ctx, alpha.Team(), "run-parent", tok.NodeID+"-sibling", "child-probe"); err != nil || got {
		t.Fatalf("a sibling node reads the child as its own: %v %v", got, err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	req := launchRequest()
	req.RunID, req.NodeID = "run-parent", "a"
	retry, err := st.ClaimLaunch(ctx, launcherIdentity, req, time.Now())
	if err != nil || retry == nil {
		t.Fatalf("claim the retry: %+v %v", retry, err)
	}
	if tok, err = st.AuthorizeClaimToken(ctx, retry.Token, store.ClaimReporting, time.Now()); err != nil {
		t.Fatal(err)
	}
	child(0, "child-probe", "probe")
}

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

// The secret release is fenced like any sensitive write: the claim must be a
// live work claim of a run that is not being cancelled, and the name must be
// one the accepted plan declares.
func TestReleaseClaimSecret_FencesTheClaimAndTheDeclaredName(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-secret")
	if err := f.s.CreateOrReplaceSecret(store.Secret{Name: "TOK", Value: "v", Pipeline: "demo"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	team, err := f.s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	_, planTok := f.launch(t, time.Now())
	if _, err := team.ReleaseClaimSecret(ctx, planTok, "TOK", time.Now()); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("a planning claim's release: err = %v, want ErrClaimNotLive", err)
	}
	if _, err := f.accept(planTok, `{"nodes":[{"id":"a","deps":[],"spec_hash":"`+hashA+`"}],"secrets":[{"name":"TOK"}]}`); err != nil {
		t.Fatal(err)
	}
	_, work := f.launch(t, time.Now())
	if sec, err := team.ReleaseClaimSecret(ctx, work, "TOK", time.Now()); err != nil || sec.Value != "v" {
		t.Fatalf("declared release = %+v %v", sec, err)
	}
	if _, err := team.ReleaseClaimSecret(ctx, work, "OTHER", time.Now()); !errors.Is(err, store.ErrSecretUndeclared) {
		t.Fatalf("undeclared release: err = %v", err)
	}
	if _, err := team.ReleaseClaimSecret(ctx, work, "TOK", time.Now().Add(5*time.Minute)); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("release after the lease lapsed: err = %v, want ErrClaimNotLive", err)
	}
	if err := f.s.RequestCancel(ctx, "run-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := team.ReleaseClaimSecret(ctx, work, "TOK", time.Now()); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("release after cancel: err = %v, want ErrClaimCancelRequested", err)
	}
}
