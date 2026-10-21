package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPrivateNodeRenewalAndStartRetainExactClaimFence(t *testing.T) {
	server, host, tenant, owner := localHostFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','build','pending',1,?,?)`, []byte(`{"id":"build"}`), []byte(`{}`))
	authorized := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	award, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, Actor: engineActor(authorized), Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.AdoptClaim(t.Context(), award); err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: store.ActorHolder, CapabilityPrefix: award.Node.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Generation: award.Node.Generation}}
	if _, err := host.RenewNode(t.Context(), award.Node.Ref, actor, false); err != nil {
		t.Fatal(err)
	}
	started, err := host.TransitionNode(t.Context(), store.StartNode{Ref: award.Node.Ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))})
	if err != nil || started.Attempt != 1 {
		t.Fatalf("source acquisition renewal invalidated the unstarted node claim: %+v %#v", started, err)
	}
}
