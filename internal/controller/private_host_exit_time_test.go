package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestLocalHostReportsObservedExitToRestartedController(t *testing.T) {
	now := time.Now().UTC()
	server, host, tenant, owner, raw := localHostAwardFixture(t, teststore.WithClock(func() time.Time { return now }))
	acceptCapacityPlan(t, server, tenant, owner, "global", "queue", 1, 1, "build")
	actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	ref := runspec.NodeRef{RunID: "run", NodeID: "build"}
	if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	claim, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: actor, Policy: runstate.SourceNone})
	if err != nil || claim.Kind != runstate.Execute {
		t.Fatalf("award = %+v: %v", claim, err)
	}
	if err := host.AdoptClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	nodeActor := runstate.Actor{Kind: runstate.ActorHolder, CapabilityPrefix: claim.Node.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "build", Generation: claim.Node.Generation}}
	if _, err := host.MayStart(t.Context(), runstate.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(claim.Node.Generation)}, nodeActor); err != nil {
		t.Fatal(err)
	}
	started, err := transitionLocalNode(t.Context(), host, store.StartNode{Ref: ref, Actor: nodeActor, SourceAttempt: new(runspec.AttemptOrdinal(0))})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	exited := now
	now = now.Add(3 * time.Second)
	restarted := New(server.store, nil)
	successor, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{restarted.Handler()}}, Bearer: func(context.Context) (string, error) { return raw, nil }}, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = successor.Close() })
	if err := successor.AdoptClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	command := store.ReportNode{Ref: ref, Actor: nodeActor, SourceAttempt: new(started.Attempt), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}, ExitedAt: &exited}
	if _, err := transitionLocalNode(t.Context(), successor, command); err != nil {
		t.Fatal(err)
	}
	command.ExitedAt = &now
	if replay, err := transitionLocalNode(t.Context(), successor, command); err != nil || !replay.Replayed {
		t.Fatalf("observation-only replay = %+v: %v", replay, err)
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || row.FinishedAt == nil || !row.FinishedAt.Equal(exited) {
		t.Fatalf("job finished = %v, observed %v, received %v: %v", row.FinishedAt, exited, now, err)
	}
	var finished int64
	if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT finished_at FROM node_attempts WHERE team='a' AND run_id='run' AND node_id='build' AND attempt=1`).Scan(&finished); err != nil || finished != exited.UnixNano() {
		t.Fatalf("attempt finished = %d, observed %d: %v", finished, exited.UnixNano(), err)
	}
}

func TestNodeExitHeaderRejectsInvalidObservations(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  runstate.TransitionCause
		values []string
	}{
		{"malformed", runstate.CauseReport, []string{"invalid"}},
		{"empty", runstate.CauseReport, []string{""}},
		{"multiple", runstate.CauseReport, []string{"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"}},
		{"joined", runstate.CauseRetry, []string{"2026-01-01T00:00:00Z, 2026-01-01T00:00:00Z"}},
		{"start", runstate.CauseStart, []string{"2026-01-01T00:00:00Z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			for _, value := range test.values {
				request.Header.Add(controllerwire.NodeExitedAtHeader, value)
			}
			body := runstate.NodeCommandPayload{Cause: test.cause}
			if err := observeNodeExit(request, &body); err == nil {
				t.Fatal("invalid observed exit accepted")
			}
		})
	}
}
