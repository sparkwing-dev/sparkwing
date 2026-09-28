package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type claimRouteFixture struct {
	st      *store.Store
	srv     *Server
	handler http.Handler
	raw     string
	runner  string
}

// safety: these routes stand in for real ones; no production route admits a claim token.
func newClaimRouteFixture(t *testing.T) claimRouteFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
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
	if err := st.MarkNodeReady(ctx, "run-a", "build"); err != nil {
		t.Fatal(err)
	}
	node, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"}, "holder", time.Minute, nil)
	if err != nil || node == nil {
		t.Fatalf("claim: %v %v", node, err)
	}
	raw, err := st.MintClaimToken(ctx, store.DefaultTeam, "run-a", "build", node.ClaimGeneration,
		store.ClaimTokenWork, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}

	srv := New(st, nil).EnableAuthFromStore()
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, _ := claimTokenFromContext(r.Context())
		p, _ := PrincipalFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ended": tok.Ended, "kind": p.Kind, "scopes": len(p.Scopes)})
	})
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/probe", newClaimTokenRoute(store.ClaimSensitive, echo))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/report", newClaimTokenRoute(store.ClaimReporting, echo))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/result", newClaimTokenRoute(store.ClaimResult, echo))
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, echo))
	return claimRouteFixture{
		st: st, srv: srv, raw: raw, runner: runner,
		handler: srv.authenticated(mux, srv.teamBoundary(mux, mux)),
	}
}

func (f claimRouteFixture) call(t *testing.T, method, path, bearer string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
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
	wantStatus(t, f, http.MethodPost, resultA, f.raw, http.StatusOK, "")
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
	if body := wantStatus(t, f, http.MethodPost, resultA, f.raw, http.StatusOK, ""); body["ended"] != true {
		t.Fatalf("result route after finish: %v", body)
	}
}

// No production route is a claim route, so a claim token is refused on the
// real router exactly as any unrecognized bearer is.
func TestClaimRoute_TheControllerRouterAdmitsNoClaimToken(t *testing.T) {
	f := newClaimRouteFixture(t)
	h := f.srv.Handler()
	for _, path := range []string{"/api/v1/runs/run-a", "/api/v1/runs/run-a/nodes/build/logs", "/api/v1/secrets/x"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+f.raw)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s: status %d, want 401", path, rec.Code)
		}
	}
}
