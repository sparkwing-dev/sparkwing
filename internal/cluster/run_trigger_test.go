package cluster

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunSpecificTriggerClaimsOnlyNamedTeamRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, name := range []store.Team{"alpha", "bravo"} {
		if err := st.AsOperator().CreateTeam(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	alpha, err := st.ForTeam(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	bravo, err := st.ForTeam(ctx, "bravo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := bravo.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "bravo-run", Pipeline: "demo", RepoURL: "http://127.0.0.1/repo", CreatedAt: now,
	}, store.Run{ID: "bravo-run", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := bravo.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "cancelled-run", Pipeline: "demo", CreatedAt: now,
	}, store.Run{ID: "cancelled-run", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestCancel(ctx, "cancelled-run"); err != nil {
		t.Fatal(err)
	}
	scopes := []string{controller.ScopeTriggersClaim, controller.ScopeRunsState}
	alphaToken, _, err := alpha.CreateToken(ctx, "alpha-runner", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	bravoToken, _, err := bravo.CreateToken(ctx, "bravo-runner", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()
	opts := TriggerLoopOptions{ControllerURL: srv.URL, GitcacheURL: srv.URL, WorkRoot: t.TempDir(), Token: alphaToken}
	if err := RunSpecificTrigger(ctx, "bravo-run", opts); !errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), alphaToken) {
		t.Fatalf("alpha claim of bravo run = %v, want not found without bearer", err)
	}
	trigger, err := st.GetTrigger(ctx, "bravo-run")
	if err != nil || trigger.Status != "pending" {
		t.Fatalf("bravo run after alpha claim = %+v, %v; want pending", trigger, err)
	}
	opts.Token = bravoToken
	if err := RunSpecificTrigger(ctx, "cancelled-run", opts); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancelled claim = %v, want not found", err)
	}
	if err := RunSpecificTrigger(ctx, "bravo-run", opts); err == nil {
		t.Fatal("invalid source should fail after bravo's claim")
	}
	trigger, err = st.GetTrigger(ctx, "bravo-run")
	if err != nil || trigger.Status != "failed" || trigger.ClaimSeq != 1 {
		t.Fatalf("bravo run after own claim = %+v, %v; want failed at generation 1", trigger, err)
	}
	run, err := st.GetRun(ctx, "bravo-run")
	if err != nil || run.Status != "failed" {
		t.Fatalf("bravo run after handler failure = %+v, %v; want failed", run, err)
	}
	if err := RunSpecificTrigger(ctx, "bravo-run", opts); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("repeat claim = %v, want not found", err)
	}
}
