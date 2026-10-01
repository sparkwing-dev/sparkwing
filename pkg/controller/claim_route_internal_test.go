package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type claimRouteFixture struct {
	st      *store.Store
	srv     *Server
	handler http.Handler
	raw     string
	plan    string
	runner  string
	results *int
}

// safety: these routes stand in for one route of each class, so every class is
// exercised whichever production routes adopt it.
func newClaimRouteFixture(t *testing.T) claimRouteFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now()
	runner, _, err := st.CreateToken("runner", store.TokenKindRunner, []string{ScopeNodesClaim, ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{"run-a", "run-b"} {
		if err := st.CreateRun(ctx, store.Run{ID: run, Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateTrigger(ctx, store.Trigger{ID: run, Pipeline: "demo", Status: "claimed", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range []string{"build", "test"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: node, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	mint := func(nodeID string, kind store.ClaimTokenKind) string {
		t.Helper()
		if err := st.MarkNodeReady(ctx, "run-a", nodeID); err != nil {
			t.Fatal(err)
		}
		node, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
			"holder-"+nodeID, time.Minute, nil)
		if err != nil || node == nil || node.NodeID != nodeID {
			t.Fatalf("claim %s: %v %v", nodeID, node, err)
		}
		raw, err := st.MintClaimToken(ctx, store.DefaultTeam, "run-a", nodeID, node.ClaimGeneration, kind, now.Add(time.Hour), now)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := mint("build", store.ClaimTokenWork)
	plan := mint("test", store.ClaimTokenPlan)

	srv := New(st, nil).EnableAuthFromStore()
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, _ := claimTokenFromContext(r.Context())
		p, _ := PrincipalFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ended": tok.Ended, "kind": p.Kind, "scopes": len(p.Scopes)})
	})
	both := []store.ClaimTokenKind{store.ClaimTokenPlan, store.ClaimTokenWork}
	work := []store.ClaimTokenKind{store.ClaimTokenWork}
	results := new(int)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/probe", newClaimSensitiveRoute(work, nil, echo))
	mux.Handle("GET /api/v1/runs/{id}/source", newClaimSensitiveRoute(both, nil, echo))
	mux.Handle("GET /api/v1/secrets/{name}", newClaimSensitiveRoute(work, nil, echo))
	mux.Handle("GET /api/v1/by-query", newClaimSensitiveRoute(work,
		func(r *http.Request) (string, string) { return r.URL.Query().Get("run"), "" }, echo))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/report", newClaimReportingRoute(both, echo))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/result", srv.newClaimResultRoute(both, nil,
		func(w http.ResponseWriter, r *http.Request, commit store.ClaimResultCommit) {
			*results++
			writeJSON(w, http.StatusOK, map[string]any{"node": commit.Token().NodeID})
		}))
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, echo))
	return claimRouteFixture{
		st: st, srv: srv, raw: raw, plan: plan, runner: runner, results: results,
		handler: srv.authenticated(mux, srv.teamBoundary(mux, mux)),
	}
}

func (f claimRouteFixture) call(t *testing.T, method, path, bearer string) (int, map[string]any) {
	t.Helper()
	return f.callBody(t, method, path, bearer, "")
}

func (f claimRouteFixture) callBody(t *testing.T, method, path, bearer, payload string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

const (
	probeA  = "/api/v1/runs/run-a/probe"
	reportA = "/api/v1/runs/run-a/nodes/build/report"
	resultA = "/api/v1/runs/run-a/nodes/build/result"
)

func wantStatus(t *testing.T, f claimRouteFixture, method, path, bearer string, want int, code string) map[string]any {
	t.Helper()
	got, body := f.call(t, method, path, bearer)
	if got != want || (code != "" && body["error"] != code) {
		t.Fatalf("%s %s: status %d body %v, want %d %q", method, path, got, body, want, code)
	}
	return body
}

func TestClaimRoute_LiveTokenIsAClaimPrincipalWithNoScopes(t *testing.T) {
	f := newClaimRouteFixture(t)
	body := wantStatus(t, f, http.MethodGet, probeA, f.raw, http.StatusOK, "")
	if body["kind"] != principalKindClaim || body["scopes"] != float64(0) {
		t.Fatalf("principal = %v", body)
	}
	wantStatus(t, f, http.MethodPost, reportA, f.raw, http.StatusOK, "")
	if code, body := f.callBody(t, http.MethodPost, resultA, f.raw, "outcome"); code != http.StatusOK || body["node"] != "build" {
		t.Fatalf("live result: %d %v", code, body)
	}
}

func TestClaimRoute_TokenReachesOnlyClaimRoutesOfItsOwnClaim(t *testing.T) {
	f := newClaimRouteFixture(t)
	wantStatus(t, f, http.MethodGet, "/api/v1/runs/run-a", f.raw, http.StatusUnauthorized, "unauthenticated")
	wantStatus(t, f, http.MethodGet, "/api/v1/runs/run-b/probe", f.raw, http.StatusForbidden, "claim_mismatch")
	wantStatus(t, f, http.MethodPost, "/api/v1/runs/run-a/nodes/test/report", f.raw, http.StatusForbidden, "claim_mismatch")
	wantStatus(t, f, http.MethodPost, "/api/v1/runs/run-a/nodes/test/result", f.raw, http.StatusForbidden, "claim_mismatch")
	wantStatus(t, f, http.MethodGet, probeA, f.runner, http.StatusForbidden, "claim_token_required")
	wantStatus(t, f, http.MethodGet, probeA, f.raw+"x", http.StatusUnauthorized, "unauthenticated")
}

// The token cache would answer these requests for a minute; a claim route
// reads the claim row instead, so the request right after the change is
// refused.
func TestClaimRoute_CancelAndFinishRefuseTheVeryNextRequest(t *testing.T) {
	f := newClaimRouteFixture(t)
	ctx := context.Background()
	wantStatus(t, f, http.MethodGet, probeA, f.raw, http.StatusOK, "")
	if err := f.st.RequestCancel(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f, http.MethodGet, probeA, f.raw, http.StatusForbidden, "claim_cancelled")
	wantStatus(t, f, http.MethodPost, reportA, f.raw, http.StatusOK, "")

	if err := f.st.FinishNode(ctx, "run-a", "build", "success", "", nil); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f, http.MethodGet, probeA, f.raw, http.StatusForbidden, "claim_ended")
	wantStatus(t, f, http.MethodPost, reportA, f.raw, http.StatusForbidden, "claim_ended")
}

// On the real router a claim token reaches only the claim routes; every other
// route refuses it exactly as it refuses any unrecognized bearer.
func TestClaimRoute_OtherControllerRoutesRefuseAClaimToken(t *testing.T) {
	f := newClaimRouteFixture(t)
	h := f.srv.Handler()
	for _, path := range []string{"/api/v1/runs/run-a/receipt", "/api/v1/runs/run-a/nodes/build/logs", "/api/v1/tokens"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+f.raw)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s: status %d, want 401", path, rec.Code)
		}
	}
}

func TestClaimRoute_KindMustBeOneTheRouteDeclares(t *testing.T) {
	f := newClaimRouteFixture(t)
	wantStatus(t, f, http.MethodGet, probeA, f.plan, http.StatusForbidden, "claim_kind")
	wantStatus(t, f, http.MethodGet, "/api/v1/runs/run-a/source", f.plan, http.StatusOK, "")
	wantStatus(t, f, http.MethodGet, probeA, f.raw, http.StatusOK, "")
}

// A route with no run in its path is closed unless it binds the run itself.
func TestClaimRoute_AnUnboundRunIsRefused(t *testing.T) {
	f := newClaimRouteFixture(t)
	wantStatus(t, f, http.MethodGet, "/api/v1/secrets/token", f.raw, http.StatusForbidden, "claim_mismatch")
	wantStatus(t, f, http.MethodGet, "/api/v1/by-query", f.raw, http.StatusForbidden, "claim_mismatch")
	wantStatus(t, f, http.MethodGet, "/api/v1/by-query?run=run-b", f.raw, http.StatusForbidden, "claim_mismatch")
	wantStatus(t, f, http.MethodGet, "/api/v1/by-query?run=run-a", f.raw, http.StatusOK, "")
}

// An ended claim is answered from the result it committed and never reaches
// the handler, which is the only place a result could be written.
func TestClaimRoute_EndedResultReplaysWithoutTheHandler(t *testing.T) {
	f := newClaimRouteFixture(t)
	ctx := context.Background()
	if code, _ := f.callBody(t, http.MethodPost, resultA, f.raw, "outcome-a"); code != http.StatusOK || *f.results != 1 {
		t.Fatalf("live result: %d, handler calls %d", code, *f.results)
	}
	sum := sha256.Sum256([]byte("outcome-a"))
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE claim_tokens SET result_digest = ? WHERE node_id = 'build'`,
		hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"build", "test"} {
		if err := f.st.FinishNode(ctx, "run-a", node, "success", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if code, body := f.callBody(t, http.MethodPost, resultA, f.raw, "outcome-a"); code != http.StatusOK || body["status"] != "replayed" {
		t.Fatalf("identical replay: %d %v", code, body)
	}
	if code, _ := f.callBody(t, http.MethodPost, resultA, f.raw, "outcome-b"); code != http.StatusConflict {
		t.Fatalf("differing replay: %d", code)
	}
	if code, _ := f.callBody(t, http.MethodPost, "/api/v1/runs/run-a/nodes/test/result", f.plan, "outcome-a"); code != http.StatusConflict {
		t.Fatalf("ended claim with no committed result: %d", code)
	}
	if *f.results != 1 {
		t.Fatalf("the handler ran %d times; an ended claim must never reach it", *f.results)
	}
}
