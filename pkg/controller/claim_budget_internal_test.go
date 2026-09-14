package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestClaimBudget_AnAwardedClaimDoesNotSpendTheBudget covers a busy runner:
// the pool loop re-claims the instant it is handed a node, so demand is its
// slot count divided by node duration rather than its poll cadence. The budget
// bounds empty polling, which is what a runner can do without limit.
func TestClaimBudget_AnAwardedClaimDoesNotSpendTheBudget(t *testing.T) {
	const budget = 5
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	const awards = budget * 4
	for i := range awards {
		node := fmt.Sprintf("node-%d", i)
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: node, Status: "pending"}); err != nil {
			t.Fatalf("CreateNode: %v", err)
		}
		if err := st.MarkNodeReady(ctx, "run-1", node); err != nil {
			t.Fatalf("MarkNodeReady: %v", err)
		}
	}
	h := New(st, nil).WithRequestBudget(RequestBudget{ClaimsPerMinute: budget}).Handler()

	for i := range awards {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, namedClaimRequest("runner-1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("claim %d: status=%d, want 200; an award must not spend the claim budget", i, rec.Code)
		}
	}

	for i := range budget {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, namedClaimRequest("runner-1"))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("empty poll %d: status=%d, want 204", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, namedClaimRequest("runner-1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429; empty polling is what the budget bounds", rec.Code)
	}
}
