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
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// A runner's name is its team's to know, so the queue view shows a caller the
// runners its own team advertised and no other team's.
func TestQueueStateShowsOnlyTheCallersTeamsRunners(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, "team-b"); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil)
	now := time.Now()
	srv.runnerHeadroom.record("operator-box", runnerHeadroom{Team: store.DefaultTeam, Cores: 4, UpdatedAt: now})
	srv.runnerHeadroom.record("team-b-box", runnerHeadroom{Team: "team-b", Cores: 2, UpdatedAt: now})

	view := func(team store.Team) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/queue/state", nil)
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{
			Name: "reader", Kind: "user", Scopes: []string{ScopeRunsRead}, Team: team,
		}))
		rec := httptest.NewRecorder()
		srv.handleQueueStateView(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("queue state for %s = %d: %s", team, rec.Code, rec.Body)
		}
		var qs wingwire.QueueState
		if err := json.NewDecoder(rec.Body).Decode(&qs); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range qs.Runners {
			names = append(names, r.Name)
		}
		return names
	}
	if got := view("team-b"); len(got) != 1 || got[0] != "team-b-box" {
		t.Errorf("team B sees runners %v, want only its own", got)
	}
	if got := view(store.DefaultTeam); len(got) != 1 || got[0] != "operator-box" {
		t.Errorf("the operator's team sees runners %v, want only its own", got)
	}
}

func TestComputeLimitsNameEveryTeamsPrincipalsOnlyToTheOperator(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := New(st, nil)
	show := func(scopes ...string) computeLimitsJSON {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/compute-limits", nil)
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{Name: "p", Kind: "user", Scopes: scopes, Team: "team-b"}))
		out, err := srv.computeLimitsView(req)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := show(ScopeRunsRead).Usage.ByPrincipal; got != nil {
		t.Errorf("a team reader reads the per-principal counts %v", got)
	}
	if got := show(ScopeAdmin).Usage.ByPrincipal; got == nil {
		t.Error("the operator lost the per-principal counts")
	}
}
