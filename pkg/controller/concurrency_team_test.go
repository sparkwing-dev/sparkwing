package controller_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type waiterLeaderFake struct {
	controller.ConcurrencyStore
	tenant *store.Tenant
	seen   capability.Authorized
}

func (f *waiterLeaderFake) Team() store.Team { return f.tenant.Team() }

func (f *waiterLeaderFake) ResolveAuthorizedWaiter(ctx context.Context, a capability.Authorized, group, run, job, cache, leaderRun, leaderJob string, bypass bool) (store.WaiterResolution, error) {
	f.seen = a
	row, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: leaderRun, NodeID: leaderJob})
	if errors.Is(err, store.ErrNotFound) {
		return store.WaiterResolution{Status: store.WaiterCancelled}, nil
	}
	if err != nil {
		return store.WaiterResolution{}, err
	}
	return store.WaiterResolution{Status: store.WaiterLeaderFinished, LeaderOutcome: string(row.Outcome)}, nil
}

func TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		if err := teststore.SeedNodeFinish(f.st, t.Context(), f.teamA.Team(), store.NodeRef{RunID: f.runA, NodeID: "n1"}, store.NodeResult{Outcome: store.NodeState("failed"), MaskedError: "foreign-leader-private"}); err != nil {
			t.Fatal(err)
		}
		admitted, claim := tenantRunClaim(t, f)
		seedRun(t, f.st, f.teamB, "leader-b", "build-b")
		if err := teststore.SeedNode(f.st, t.Context(), store.Node{RunID: "leader-b", NodeID: "lead", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedNodeFinish(f.st, t.Context(), f.teamB.Team(), store.NodeRef{RunID: "leader-b", NodeID: "lead"}, store.NodeResult{Outcome: store.NodeState("success")}); err != nil {
			t.Fatal(err)
		}
		writer := &waiterLeaderFake{tenant: f.teamB}
		f.srv.WithConcurrencyStore(func(_ context.Context, team store.Team) (controller.ConcurrencyStore, error) {
			if team != f.teamB.Team() {
				t.Fatalf("foreign concurrency tenant: %s", team)
			}
			return writer, nil
		})
		var order []string
		f.srv.WithAuthorization(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(f.st), Bindings: policyBindings{order: &order}})
		resolve := func(leaderRun, leaderJob, forged string) string {
			t.Helper()
			q := url.Values{"run_id": {admitted.RunID}, "job_id": {""}, "leader_run_id": {leaderRun}, "leader_job_id": {leaderJob}, "team": {forged}}
			code, body := f.do(http.MethodGet, "/api/v1/queue/groups/deploy/resolve?"+q.Encode(), "Bearer "+claim.Capability, nil)
			if code != http.StatusOK {
				t.Fatalf("resolve %s = %d: %s", leaderRun, code, body)
			}
			a := writer.seen
			if a.Target.Team != string(f.teamB.Team()) || a.Principal.Capability.Team != string(f.teamB.Team()) || a.Target.SlotKey != "deploy" || a.Target.SlotRunID != admitted.RunID || a.Target.SlotNodeID != "" || a.Target.SlotGeneration != claim.Binding.Generation {
				t.Fatalf("changed waiter authority: %+v", a)
			}
			return body
		}
		if body := resolve("leader-b", "lead", string(f.teamA.Team())); !strings.Contains(body, `"status":"leader_finished"`) || !strings.Contains(body, `"leader_outcome":"success"`) {
			t.Fatalf("own leader: %s", body)
		}
		missing := resolve("missing", "n1", "")
		property := func(forged string) bool {
			body := resolve(f.runA, "n1", forged)
			return body == missing && strings.Contains(body, `"status":"cancelled"`) && !strings.Contains(body, "leader_outcome") && !strings.Contains(body, "foreign-leader-private")
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
	})
}
