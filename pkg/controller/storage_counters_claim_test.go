package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The logs service forwards the node's claim token to the counter routes,
// which count the claim's own team's logs with it, as they do for a
// forwarded logs.write credential, and refuse it once the claim ends.
func TestTheCounterRoutesCountAForwardedNodeClaimTokensOwnTeam(t *testing.T) {
	f := freeTierFixture(t, 5)
	ctx := context.Background()
	for _, team := range []store.Team{"first", "second"} {
		if code := f.trigger(freeTeamToken(t, f.store, team)); code != http.StatusAccepted {
			t.Fatalf("%s's run = %d", team, code)
		}
	}
	tenant, err := f.store.ForTeam(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	runner, _, err := tenant.CreateToken(ctx, "pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeLogsWrite,
	}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.CreateRun(ctx, store.Run{ID: "logged", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "logged", NodeID: "only", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := tenant.MarkNodeReady(ctx, "logged", "only"); err != nil {
		t.Fatal(err)
	}
	n, err := client.NewWithToken(f.url, nil, runner).ClaimNode(ctx, "pool:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.ClaimToken == "" {
		t.Fatalf("claim node: %+v, %v", n, err)
	}
	auth := "Bearer " + n.ClaimToken
	counter := storagequota.New(f.url, nil)
	res, err := counter.Reserve(ctx, auth, "first", storagequota.KindLogs, 10, true)
	if err != nil {
		t.Fatalf("a node claim token reserving its own team's logs: %v", err)
	}
	if _, err := counter.Renew(ctx, auth, res, 10, 10); err != nil {
		t.Fatalf("a node claim token committing its own team's logs: %v", err)
	}
	spare, err := counter.Reserve(ctx, auth, "first", storagequota.KindLogs, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := counter.Release(ctx, auth, spare); err != nil {
		t.Fatalf("a node claim token releasing its own team's reservation: %v", err)
	}
	for _, c := range []struct {
		team string
		kind storagequota.Kind
	}{{"second", storagequota.KindLogs}, {"first", storagequota.KindCache}} {
		if _, err := counter.Reserve(ctx, auth, c.team, c.kind, 10, true); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("a node claim token reserving %s's %s: %v, want 403", c.team, c.kind, err)
		}
	}
	if code := f.call("POST", "/api/v1/runs/logged/nodes/only/finish", auth, map[string]any{"outcome": "success"}, nil); code/100 != 2 {
		t.Fatalf("finish with the claim token = %d", code)
	}
	if _, err := counter.Reserve(ctx, auth, "first", storagequota.KindLogs, 10, true); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a node claim token after its claim ended: %v, want 401", err)
	}
}
