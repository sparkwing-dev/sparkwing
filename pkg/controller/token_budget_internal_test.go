package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	testAgentName   = "mine"
	testAgentPrefix = "swr_mine"
)

func newEnrolledAgentServer(t *testing.T, budget TokenRequestBudget) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.EnrollExecutor(context.Background(), testAgentPrefix, store.Executor{
		Name: testAgentName, Kind: "agent", Location: "local",
		Principal: "agent-principal", MaxConcurrent: 1,
	}); err != nil {
		t.Fatalf("EnrollExecutor: %v", err)
	}
	return New(st, nil).
		WithTokenRequestBudget(budget).
		WithPeerPrincipal(func(*http.Request) *Principal {
			return &Principal{
				Name: "agent-principal", Kind: "runner", TokenPrefix: testAgentPrefix,
				Scopes: []string{ScopeRunsRead, ScopeNodesClaim},
			}
		})
}

func agentBeat(name string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+name+"/heartbeat",
		strings.NewReader(`{"headroom":{"cores":1,"memory_bytes":1024,"queue_depth":0}}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestTokenRequestBudget_SparesOnlyTheAgentsOwnLivenessHeartbeat is the lane a
// URL-only exemption left open: any token could spend unbudgeted requests by
// naming somebody else's agent on the one route the budget skipped.
func TestTokenRequestBudget_SparesOnlyTheAgentsOwnLivenessHeartbeat(t *testing.T) {
	const budget = 10
	srv := newEnrolledAgentServer(t, TokenRequestBudget{PerTokenMinute: budget})
	h := srv.Handler()

	for i := range budget {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was shed inside the budget", i)
		}
	}

	const flood = 200
	admitted := 0
	for i := range flood {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, agentBeat(fmt.Sprintf("not-mine-%d", i)))
		if rec.Code != http.StatusTooManyRequests {
			admitted++
		}
	}
	if admitted != 0 {
		t.Errorf("%d of %d beats for agents this token did not enroll were admitted past the budget",
			admitted, flood)
	}

	for i := range 20 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, agentBeat(testAgentName))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("beat %d for this token's own agent was shed; an agent treats that as fatal", i)
		}
	}
}

func TestTokenRequestBudget_LivenessSurvivesExhaustedToken(t *testing.T) {
	srv := newEnrolledAgentServer(t, TokenRequestBudget{PerTokenMinute: 1})
	srv.WithPeerPrincipal(func(*http.Request) *Principal {
		return &Principal{Name: "owner", Kind: "runner", TokenPrefix: testAgentPrefix, Scopes: []string{ScopeNodesClaim, ScopeTriggersClaim}}
	})
	ctx := context.Background()
	if err := srv.store.CreateTrigger(ctx, store.Trigger{ID: "old-run", Pipeline: "p", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claim, err := srv.store.ClaimNextTriggerFor(ctx, store.ClaimIdentity{Principal: "owner", TokenPrefix: testAgentPrefix}, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateRun(ctx, store.Run{ID: "old-run", Pipeline: "p", Status: "running", StartedAt: time.Now().Add(-4 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateNode(ctx, store.Node{RunID: "old-run", NodeID: "work", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateRun(ctx, store.Run{ID: "silent-run", Pipeline: "p", Status: "running", StartedAt: time.Now().Add(-4 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-run", "silent-run"} {
		if err := srv.store.TouchRunHeartbeat(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.store.DB().ExecContext(ctx, `UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`, time.Now().Add(-4*time.Minute).UnixNano(), id); err != nil {
			t.Fatal(err)
		}
	}
	h := srv.Handler()
	call := func(method, path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		if method == http.MethodPost {
			req.Header.Set(store.TriggerGenerationHeader, fmt.Sprint(claim.ClaimSeq))
		}
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	call(http.MethodGet, "/api/v1/runs")
	if got := call(http.MethodGet, "/api/v1/runs"); got != http.StatusTooManyRequests {
		t.Fatalf("ordinary request past token limit = %d, want 429", got)
	}
	missingFence := httptest.NewRecorder()
	h.ServeHTTP(missingFence, httptest.NewRequest(http.MethodPost, "/api/v1/runs/old-run/heartbeat", nil))
	if missingFence.Code != http.StatusTooManyRequests {
		t.Fatalf("heartbeat without its claim fence past token limit = %d, want 429", missingFence.Code)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/triggers/old-run/heartbeat", http.StatusOK},
		{"/api/v1/runs/old-run/heartbeat", http.StatusNoContent},
		{"/api/v1/runs/old-run/nodes/work/touch", http.StatusNoContent},
	} {
		if got := call(http.MethodPost, tc.path); got != tc.want {
			t.Errorf("liveness %s past token limit = %d, want %d", tc.path, got, tc.want)
		}
	}
	ids, err := store.Maintenance.ReapStaleRunningRuns(srv.store, ctx, 3*time.Minute, "stale")
	if err != nil || len(ids) != 1 || ids[0] != "silent-run" {
		t.Fatalf("reaper after accepted run heartbeat = %v, %v; want only silent-run reaped", ids, err)
	}
}
