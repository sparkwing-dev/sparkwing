package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func twoTeamAdviceServer(t *testing.T) (srv *Server, queueJob func()) {
	t.Helper()
	engine, _ := engineRuntimeStore(t)
	engine.claimProfiles = engineProfileFake{}
	engine.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	engineSQL(t, engine.Store, `INSERT INTO teams(name,created_at,updated_at,next_run_number) VALUES ('b',1,1,1)`)
	for prefix, team := range map[string]string{"busy": "a", "busy-peer": "a", "idle": "b"} {
		engineHTTPRoot(t, engine, team, prefix, []capability.Grant{capability.NodesClaim})
	}
	queueJob = func() {
		engineSQL(t, engine.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
		engineAcceptedFixturePlan(t, engine, "a", "run")
	}
	return New(engine.Store, nil).WithEngineController(engine.EngineController), queueJob
}

func claimAs(t *testing.T, srv *Server, prefix string) (*httptest.ResponseRecorder, controllerwire.ClaimResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/claim", strings.NewReader(`{"runner_version":"v1.2.3","kinds":["node"]}`))
	req.Header.Set("Content-Type", "application/json")
	secret := sha256.Sum256([]byte(prefix))
	req.Header.Set("Authorization", "Bearer "+capability.ControllerPrefix+base64.RawURLEncoding.EncodeToString(secret[:]))
	req.Header.Set(store.RunnerIdentityHeader, prefix)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var response controllerwire.ClaimResponse
	if rec.Code != http.StatusOK {
		t.Fatalf("%s claim: status=%d %s", prefix, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return rec, response
}

func TestAnotherTeamsAwardLeavesAnIdleTeamsPollAdvice(t *testing.T) {
	srv, queueJob := twoTeamAdviceServer(t)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))
	queueJob()

	if _, awarded := claimAs(t, srv, "busy"); awarded.Claim == nil {
		t.Fatal("team a's runner found no work to take")
	}
	idle, response := claimAs(t, srv, "idle")
	if response.Claim != nil {
		t.Fatalf("team b's runner took team a's work: %+v", response.Claim)
	}
	if got := idle.Header().Get(controllerwire.ClaimPollAfterHeader); got != "5" {
		t.Fatalf("%s=%q after another team's award; an idle team keeps the advice of its own idle clock",
			controllerwire.ClaimPollAfterHeader, got)
	}
}

func TestATeamsOwnAwardReturnsItsRunnersToTheirOwnCadence(t *testing.T) {
	srv, queueJob := twoTeamAdviceServer(t)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

	before, _ := claimAs(t, srv, "busy-peer")
	if got := before.Header().Get(controllerwire.ClaimPollAfterHeader); got == "" {
		t.Fatal("an idle team's runner got no advice; the ramp never started")
	}
	queueJob()
	if _, awarded := claimAs(t, srv, "busy"); awarded.Claim == nil {
		t.Fatal("team a's runner found no work to take")
	}
	after, _ := claimAs(t, srv, "busy-peer")
	if got := after.Header().Get(controllerwire.ClaimPollAfterHeader); got != "" {
		t.Fatalf("%s=%q right after the team's own award; its runners must return to their own cadence",
			controllerwire.ClaimPollAfterHeader, got)
	}
}

func TestATeamsOwnScheduledRunReturnsOnlyItsRunnersToTheirOwnCadence(t *testing.T) {
	st := newStoreForTick(t)
	sched := armPushedForTick(t, st)
	srv := New(st, nil)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

	if _, err := (cronLauncher{server: srv}).Launch(t.Context(), sched, time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if got := srv.claimPollAdvice(sched.Team, now); got != 0 {
		t.Errorf("the scheduled team's advice=%s right after its run arrived, want none", got)
	}
	if got := srv.claimPollAdvice("", now); got != 0 {
		t.Errorf("a claimant serving every team got advice=%s right after a run arrived, want none", got)
	}
	if got := srv.claimPollAdvice("another-team", now); got != DefaultMaxIdleClaimPoll {
		t.Errorf("another team's advice=%s after the scheduled run, want %s", got, DefaultMaxIdleClaimPoll)
	}
}

func TestATeamClockKeepsItsNewestReadingWhenWritesArriveOutOfOrder(t *testing.T) {
	srv := New(nil, nil)
	now := time.Now()
	srv.recordQueueActivity(now.Add(-testIdleWindow))
	srv.recordTeamQueueActivity("a", now)
	srv.recordTeamQueueActivity("a", now.Add(-testIdleWindow))
	if got := srv.claimPollAdvice("a", now); got != 0 {
		t.Errorf("a slow admission's older time replaced the team's fresh award: advice=%s, want none", got)
	}
	if got := srv.claimPollAdvice("", now); got != 0 {
		t.Errorf("a claimant serving every team lost the fresh award: advice=%s, want none", got)
	}
}

func TestPruneForgetsOnlyTeamClocksThatCanNoLongerChangeAdvice(t *testing.T) {
	srv := New(nil, nil)
	now := time.Now()
	srv.recordQueueActivity(now.Add(-time.Hour))
	settledAt := now.Add(-(idleClaimPollFraction+1)*DefaultMaxIdleClaimPoll - time.Second)
	srv.recordTeamQueueActivity("settled", settledAt)
	srv.recordTeamQueueActivity("recent", now.Add(-time.Second))
	srv.recordTeamQueueActivity("ramping", now.Add(-(idleClaimPollFraction+1)*DefaultMaxIdleClaimPoll+time.Second))
	before := map[store.Team][2]time.Duration{}
	for _, team := range []store.Team{"settled", "recent", "ramping"} {
		before[team] = [2]time.Duration{srv.claimPollAdvice(team, now), srv.claimPollAdvice(team, now.Add(-srv.idleClaimPoll))}
	}

	srv.queueActivity.prune(now, srv.idleClaimPoll)

	if _, kept := srv.queueActivity.teams["settled"]; kept {
		t.Error("a team idle past the enforced ceiling was kept")
	}
	for _, team := range []store.Team{"recent", "ramping"} {
		if _, kept := srv.queueActivity.teams[team]; !kept {
			t.Errorf("team %s, whose advice can still change, was forgotten", team)
		}
	}
	for team, want := range before {
		if got := [2]time.Duration{srv.claimPollAdvice(team, now), srv.claimPollAdvice(team, now.Add(-srv.idleClaimPoll))}; got != want {
			t.Errorf("team %s advice changed across pruning: %v, want %v", team, got, want)
		}
	}
}

func TestAnApprovedJobReturnsItsTeamsRunnersToTheirOwnCadence(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl, capability.ApprovalsWrite}
	f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, nil)})
	testOnlyTenantPort[runControlWriter](f.s, func(context.Context, store.Team) (runControlWriter, error) {
		return &runControlWriterFake{team: store.DefaultTeam}, nil
	})
	f.s.recordQueueActivity(time.Now().Add(-testIdleWindow))
	approve := func(resolution string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/bound-run/approvals/bound-job", strings.NewReader(`{"resolution":"`+resolution+`"}`))
		r.Header.Set("Authorization", "Bearer "+f.raw)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", resolution)
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", resolution, w.Code, w.Body)
		}
	}
	approve(store.ApprovalResolutionDenied)
	if got := f.s.claimPollAdvice(store.DefaultTeam, time.Now()); got == 0 {
		t.Fatal("a denial, which queues no work, returned the team's runners to their own cadence")
	}
	approve(store.ApprovalResolutionApproved)
	if got := f.s.claimPollAdvice(store.DefaultTeam, time.Now()); got != 0 {
		t.Fatalf("advice=%s right after an approval released the job; the team's runners must return to their own cadence", got)
	}
	if got := f.s.claimPollAdvice("another-team", time.Now()); got == 0 {
		t.Fatal("another team's runners lost their idle advice to this team's approval")
	}
}

func TestAReplayedApprovalLeavesTheIdleClockAlone(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl, capability.ApprovalsWrite}
	f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, nil)})
	testOnlyTenantPort[runControlWriter](f.s, func(context.Context, store.Team) (runControlWriter, error) {
		return &runControlWriterFake{team: store.DefaultTeam, replayed: true}, nil
	})
	f.s.recordQueueActivity(time.Now().Add(-testIdleWindow))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/bound-run/approvals/bound-job", strings.NewReader(`{"resolution":"approved"}`))
	r.Header.Set("Authorization", "Bearer "+f.raw)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "approve")
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("replayed approval: %d %s", w.Code, w.Body)
	}
	for _, team := range []store.Team{store.DefaultTeam, ""} {
		if got := f.s.claimPollAdvice(team, time.Now()); got == 0 {
			t.Fatalf("a replayed approval, which releases nothing new, returned claimants of team %q to their own cadence", team)
		}
	}
}

func TestPruneNeverChangesAdviceOrEnforcementWhateverTheCeiling(t *testing.T) {
	for _, ceiling := range []time.Duration{time.Second, 2 * time.Second, 2500 * time.Millisecond, DefaultMaxIdleClaimPoll, 7300 * time.Millisecond} {
		srv := New(nil, nil).WithIdleClaimPoll(ceiling)
		now := time.Now()
		srv.recordQueueActivity(now.Add(-time.Hour))
		for idle := time.Duration(0); idle <= 60*time.Second; idle += 250 * time.Millisecond {
			team := store.Team(idle.String())
			srv.recordTeamQueueActivity(team, now.Add(-idle))
		}
		read := func(team store.Team) [2]time.Duration {
			return [2]time.Duration{srv.claimPollAdvice(team, now), srv.claimPollAdvice(team, now.Add(-ceiling))}
		}
		before := map[store.Team][2]time.Duration{}
		for team := range srv.queueActivity.teams {
			before[team] = read(team)
		}
		srv.queueActivity.prune(now, ceiling)
		for team, want := range before {
			if got := read(team); got != want {
				t.Errorf("ceiling %s: pruning team idle %s changed advice and the enforcement lookback from %v to %v", ceiling, team, want, got)
			}
		}
	}
}

func TestAReplayedScheduledRunLeavesTheIdleClocksAlone(t *testing.T) {
	st := newStoreForTick(t)
	sched := armPushedForTick(t, st)
	srv := New(st, nil)
	launcher := cronLauncher{server: srv}
	due := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	first, err := launcher.Launch(t.Context(), sched, due)
	if err != nil {
		t.Fatal(err)
	}
	idle := time.Now().Add(-testIdleWindow).UnixNano()
	srv.queueActivity.mu.Lock()
	srv.queueActivity.all, srv.queueActivity.any, srv.queueActivity.teams = idle, idle, nil
	srv.queueActivity.mu.Unlock()

	replayed, err := launcher.Launch(t.Context(), sched, due)
	if err != nil || replayed != first {
		t.Fatalf("the repeated instant started %s (%v), want the replay of %s", replayed, err, first)
	}
	for _, team := range []store.Team{sched.Team, ""} {
		if got := srv.claimPollAdvice(team, time.Now()); got == 0 {
			t.Fatalf("a replayed scheduled run, which queues nothing new, returned claimants of team %q to their own cadence", team)
		}
	}
}
