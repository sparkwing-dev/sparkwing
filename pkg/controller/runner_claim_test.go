package controller_test

import (
	"context"
	"encoding/json"
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
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type runnerClaimFixture struct {
	st     *store.Store
	url    string
	runner string
}

func newRunnerClaimFixture(t *testing.T) runnerClaimFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now()
	runner, _, err := st.CreateToken("runner-a", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState, controller.ScopeLogsWrite,
	}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run-1", Pipeline: "demo", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedRunNode(t, st, "run-1", "only")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "other", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "only"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePlanSnapshot(ctx, "run-1",
		[]byte(`{"nodes":[],"secrets":[{"name":"DEPLOY_TOKEN","required":true}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DEPLOY_TOKEN", "UNDECLARED"} {
		if err := st.CreateOrReplaceSecret(store.Secret{Name: name, Value: "v-" + name, Pipeline: "demo", Masked: true}, now); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return runnerClaimFixture{st: st, url: srv.URL, runner: runner}
}

func (f runnerClaimFixture) do(t *testing.T, method, path, token, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// A runner's node claim answers with a claim token. The token carries the
// node's own routes with no fence of its own, reaches nothing outside its run
// or its node's writes, and stops working the moment the claim ends.
func TestRunnerClaim_NodeTokenServesItsNodeUntilTheClaimEnds(t *testing.T) {
	f := newRunnerClaimFixture(t)
	n, err := client.NewWithToken(f.url, nil, f.runner).ClaimNode(context.Background(), "runner:box-a:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.NodeID != "only" {
		t.Fatalf("claim = %+v, %v", n, err)
	}
	tok := n.ClaimToken
	if !strings.HasPrefix(tok, store.ClaimTokenPrefix+"_") {
		t.Fatalf("claim token = %q, want a %s_ token", tok, store.ClaimTokenPrefix)
	}
	for _, c := range []struct {
		method, path, body string
		want               int
		code               string
	}{
		{"GET", "/api/v1/runs/run-1", "", http.StatusOK, ""},
		{"GET", "/api/v1/triggers/run-1", "", http.StatusOK, ""},
		{"POST", "/api/v1/runs/run-1/nodes/only/annotations", `{"message":"hi"}`, http.StatusNoContent, ""},
		{"POST", "/api/v1/runs/run-1/nodes/other/annotations", `{"message":"hi"}`, http.StatusForbidden, "claim_mismatch"},
		{"POST", "/api/v1/runs/run-1/nodes/other/mark-ready", "", http.StatusForbidden, "claim_mismatch"},
		{"POST", "/api/v1/runs/run-1/finish", `{"status":"success"}`, http.StatusForbidden, "claim_mismatch"},
		{"GET", "/api/v1/runs/run-2", "", http.StatusForbidden, "claim_mismatch"},
		{"POST", "/api/v1/nodes/claim", `{"holder_id":"x"}`, http.StatusForbidden, "claim_mismatch"},
		{"GET", "/api/v1/secrets/UNDECLARED?run=run-1", "", http.StatusForbidden, "secret_undeclared"},
	} {
		if got, body := f.do(t, c.method, c.path, tok, c.body); got != c.want || (c.code != "" && body["error"] != c.code) {
			t.Errorf("%s %s = %d %v, want %d %s", c.method, c.path, got, body, c.want, c.code)
		}
	}
	// safety: a fence the pod names is replaced by its claim's own, so
	// naming a later generation does not move the request off its claim.
	if got, body := f.do(t, "POST", "/api/v1/runs/run-1/nodes/only/annotations", tok, `{"message":"again"}`,
		store.ClaimGenerationHeader, "99", store.TriggerGenerationHeader, "1"); got != http.StatusNoContent {
		t.Fatalf("a request naming another fence = %d %v, want its own claim's", got, body)
	}
	if got, body := f.do(t, "POST", "/api/v1/runs/run-1/nodes/only/finish", tok, `{"outcome":"success"}`); got/100 != 2 {
		t.Fatalf("finish with the claim token = %d %v", got, body)
	}
	if got, body := f.do(t, "POST", "/api/v1/runs/run-1/nodes/only/annotations", tok, `{"message":"late"}`); got != http.StatusForbidden || body["error"] != "claim_ended" {
		t.Fatalf("a write after the claim ended = %d %v, want 403 claim_ended", got, body)
	}
	if got, _ := f.do(t, "GET", "/api/v1/runs/run-1", tok, ""); got != http.StatusForbidden {
		t.Fatalf("a read after the claim ended = %d, want 403", got)
	}
}

// The token acts as the runner that made the claim, so revoking the runner's
// credential stops every token its claims hold.
func TestRunnerClaim_RevokingTheRunnerStopsItsClaimTokens(t *testing.T) {
	f := newRunnerClaimFixture(t)
	n, err := client.NewWithToken(f.url, nil, f.runner).ClaimNode(context.Background(), "runner:box-a:1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim = %+v, %v", n, err)
	}
	if err := f.st.RevokeToken(f.runner[:store.PrefixLen], time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, body := f.do(t, "GET", "/api/v1/runs/run-1", n.ClaimToken, ""); got != http.StatusUnauthorized {
		t.Fatalf("a claim token of a revoked runner = %d %v, want 401", got, body)
	}
}

// A trigger claim answers with a claim token that reads the run's declared
// secrets, records each release, refuses an undeclared name and stops when
// the trigger is finished.
func TestRunnerClaim_TriggerTokenReadsDeclaredSecretsWhileItsClaimLives(t *testing.T) {
	f := newRunnerClaimFixture(t)
	ctx := context.Background()
	trig, err := client.NewWithToken(f.url, nil, f.runner).ClaimSpecificTrigger(ctx, "run-1", time.Minute)
	if err != nil || trig == nil {
		t.Fatalf("claim trigger = %+v, %v", trig, err)
	}
	tok := trig.ClaimToken
	if !strings.HasPrefix(tok, store.ClaimTokenPrefix+"_") {
		t.Fatalf("trigger claim token = %q", tok)
	}
	if got, body := f.do(t, "GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-1", tok, ""); got != http.StatusOK || body["value"] != "v-DEPLOY_TOKEN" {
		t.Fatalf("declared secret = %d %v", got, body)
	}
	if got, body := f.do(t, "GET", "/api/v1/secrets/UNDECLARED?run=run-1", tok, ""); got != http.StatusForbidden || body["error"] != "secret_undeclared" {
		t.Fatalf("undeclared secret = %d %v, want 403 secret_undeclared", got, body)
	}
	if got, body := f.do(t, "GET", "/api/v1/runs/run-1", tok, ""); got != http.StatusForbidden || body["error"] != "claim_kind" {
		t.Fatalf("a trigger claim token on a run route = %d %v, want 403 claim_kind", got, body)
	}
	events, err := f.st.ListEventsAfter(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	released := 0
	for _, e := range events {
		if e.Kind == "secret_released" && strings.Contains(string(e.Payload), "DEPLOY_TOKEN") {
			released++
		}
	}
	if released != 1 {
		t.Fatalf("recorded %d secret releases, want 1", released)
	}
	if err := f.st.FinishTrigger(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	if got, body := f.do(t, "GET", "/api/v1/secrets/DEPLOY_TOKEN?run=run-1", tok, ""); got != http.StatusForbidden || body["error"] != "claim_ended" {
		t.Fatalf("a secret read after the trigger finished = %d %v, want 403 claim_ended", got, body)
	}
}
