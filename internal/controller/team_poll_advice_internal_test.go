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

// twoTeamAdviceServer has two runners in team a and one in team b; queueJob queues one job in team a.
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
