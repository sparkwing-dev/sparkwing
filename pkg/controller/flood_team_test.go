package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A signed-up team's tokens share one run-creation bucket, so minting another
// token does not buy another cap, and a retry spends from the same bucket as
// a trigger.
func TestFloodPolicy_ATeamsTokensShareOneCap(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now); err != nil {
		t.Fatal(err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore().WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 2})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for _, name := range []string{"one", "two", "three"} {
		raw, _, err := acme.CreateToken(ctx, name, store.TokenKindUser,
			[]string{controller.ScopeRunsWrite, controller.ScopeRunsControl, controller.ScopeRunsRead}, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, "Bearer "+raw)
	}
	f := &tenancyFixture{t: t, url: ts.URL, st: st}
	trigger := func(auth string, seed int) int {
		code, _ := f.do("POST", "/api/v1/triggers", auth, map[string]any{
			"pipeline": "build", "trigger": map[string]any{"source": "api"},
			"git": map[string]any{"branch": "main", "sha": fortyHex(seed)},
		})
		return code
	}
	if code := trigger(tokens[0], 1); code != http.StatusAccepted {
		t.Fatalf("first trigger = %d", code)
	}
	seedRun(t, acme, "run-acme", "build")
	if code, body := f.do("POST", "/api/v1/runs/run-acme/retry", tokens[1], nil); code/100 != 2 {
		t.Fatalf("retry inside the cap = %d: %s", code, body)
	}
	if code := trigger(tokens[2], 3); code != http.StatusTooManyRequests {
		t.Errorf("a third token's trigger past the team's cap = %d want 429", code)
	}
	if code, _ := f.do("POST", "/api/v1/runs/run-acme/retry", tokens[2], nil); code != http.StatusTooManyRequests {
		t.Errorf("a retry past the team's cap = %d want 429", code)
	}
}
