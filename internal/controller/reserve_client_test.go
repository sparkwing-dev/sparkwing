package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestShippedClientRenewsAndStartsThroughAnEmptyTeamBucket(t *testing.T) {
	server, owner, tenant, ownerPrincipal, ownerBearer := localHostAwardFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','build','pending',1,?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	authorized := capability.Authorized{Principal: ownerPrincipal, Binding: ownerPrincipal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	award, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: runspec.NodeRef{RunID: "run", NodeID: "build"}, Actor: engineActor(authorized), Policy: runstate.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.AdoptClaim(t.Context(), award); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{handler}}, Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}
	node, err := client.NewNodeHostClient(options, "run", "build")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if _, err := node.Principal(t.Context()); err != nil {
		t.Fatal(err)
	}
	engineAcceptedFixturePlan(t, server.engine, "a", "run", "build")
	actor := runstate.Actor{Kind: runstate.ActorHolder, CapabilityPrefix: award.Node.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "build", Generation: award.Node.Generation}}
	if result, err := owner.MayStart(t.Context(), runstate.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(award.Node.Generation)}, actor); err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("execution admission: %+v, %v", result, err)
	}
	controller, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}

	server.WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 32})
	drained := false
	for range 64 {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run/local-host/snapshot", nil)
		r.Header.Set("Authorization", "Bearer "+ownerBearer)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if drained = w.Code == http.StatusTooManyRequests; drained {
			break
		}
	}
	if !drained {
		t.Fatal("the run host's snapshot reads never emptied the team bucket")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ref := runspec.NodeRef{RunID: "run", NodeID: "build"}
	for name, private := range map[string]nodehost.PrivateHostState{"node host": node, "owner of an adopted node": owner} {
		state, err := nodehost.NewRemoteHostStore(controller, private, "unit", func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if beat, err := state.RenewNodeClaim(ctx, ref, actor); err != nil || beat.ExpiresAt.IsZero() {
			t.Fatalf("%s renewal with an empty team bucket: %+v, %v", name, beat, err)
		}
	}
	state, err := nodehost.NewRemoteHostStore(controller, owner, "unit", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if started, err := state.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))}); err != nil || started.Attempt != 1 {
		t.Fatalf("owner start with an empty team bucket: %+v, %v", started, err)
	}
}

func TestEvalCredentialOpensApprovalThroughAnEmptyTeamBucket(t *testing.T) {
	server, _, tenant, ownerPrincipal := localHostFixture(t)
	gate := nodewire.JobSpec{ID: "gate", Approval: &nodewire.ApprovalSpec{}, Closures: nodewire.ClosureSpec{SkipIf: []string{"skip"}}}
	spec, err := json.Marshal(gate)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','gate','pending',?,?)`, spec, []byte(`{}`))
	engineAcceptedFixturePlanDoc(t, server.engine, "a", "run", nodewire.PlanDoc{Jobs: []nodewire.JobSpec{gate}})
	ref := runspec.NodeRef{RunID: "run", NodeID: "gate"}
	owner := engineActor(capability.Authorized{Principal: ownerPrincipal, Binding: ownerPrincipal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	if _, err := tenant.ReleaseEvaluation(t.Context(), ref, owner); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','gate',0,'pending') ON CONFLICT DO NOTHING`)
	claim, err := server.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "gate"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16}).Handler()
	drained := false
	for range 32 {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run/local-host/snapshot", nil)
		r.Header.Set("Authorization", "Bearer "+string(claim.Bearer))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if drained = w.Code == http.StatusTooManyRequests; drained {
			break
		}
	}
	if !drained {
		t.Fatal("the evaluator's snapshot reads never emptied the team bucket")
	}
	host, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{handler}}, Bearer: func(context.Context) (string, error) { return string(claim.Bearer), nil }}, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := transitionLocalNode(ctx, host, store.OpenApprovalNode{Ref: ref, Actor: actor}); err != nil {
		t.Fatalf("approval transition with an empty team bucket: %v", err)
	}
	if row, err := tenant.ReadNode(t.Context(), ref); err != nil || row.Status != runstate.StatusApprovalPending {
		t.Fatalf("approval did not commit: %+v, %v", row, err)
	}
}
