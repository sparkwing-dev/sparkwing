package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
