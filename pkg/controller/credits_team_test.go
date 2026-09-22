package controller_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func creditsBlockedEvents(t *testing.T, st *store.Store, runID string) int {
	t.Helper()
	events, err := st.ListEventsAfter(context.Background(), runID, 0, 50)
	if err != nil {
		t.Fatalf("list events for %s: %v", runID, err)
	}
	seen := 0
	for _, e := range events {
		if e.Kind == store.EventKindCreditsBlocked {
			seen++
		}
	}
	return seen
}

// A credit refusal is recorded on the node the claimant was refused for.
// The other team's node waits longer, so a refusal pinned to the oldest
// waiting node would surface one team's empty balance on another's run.
func TestCreditRefusalLandsOnTheRefusedTeamsRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	teams := map[store.Team]*store.Tenant{}
	for _, name := range []store.Team{"alpha", "bravo"} {
		if err := st.AsOperator().CreateTeam(ctx, name); err != nil {
			t.Fatal(err)
		}
		tn, err := st.ForTeam(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		teams[name] = tn
	}
	for _, seed := range []struct {
		team  store.Team
		runID string
	}{{"alpha", "run-alpha"}, {"bravo", "run-bravo"}} {
		if err := teams[seed.team].CreateRun(ctx, store.Run{
			ID: seed.runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: seed.runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkNodeReady(ctx, seed.runID, "build"); err != nil {
			t.Fatal(err)
		}
	}
	raw, _, err := teams["bravo"].CreateTokenWith(ctx, "agent:bravo-cloud", store.TokenKindRunner,
		runnerScopes, 0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	c := client.NewWithToken(srv.URL, nil, raw)

	if _, err := c.ClaimNode(ctx, "pod-bravo", nil, time.Minute, nil); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("bravo's claim on an empty balance = %v, want ErrInsufficientCredits", err)
	}
	if got := creditsBlockedEvents(t, st, "run-bravo"); got != 1 {
		t.Errorf("credits_blocked events on bravo's refused run = %d, want 1", got)
	}
	if got := creditsBlockedEvents(t, st, "run-alpha"); got != 0 {
		t.Errorf("bravo's refusal was recorded %d times on alpha's run", got)
	}
}
