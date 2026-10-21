package controller

import (
	"context"
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func FuzzPrivateNodeHostConfinesStateAndAuthority(f *testing.F) {
	f.Add("build", "run", int64(0))
	f.Add("build", "run", int64(1))
	f.Add("sibling", "run", int64(0))
	f.Add("build", "foreign", int64(0))
	f.Fuzz(func(t *testing.T, node, run string, sourceAttempt int64) {
		server, _, tenant, owner := localHostFixture(t)
		for _, id := range []string{"build", "sibling"} {
			engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(`{"id":"`+id+`"}`), []byte(`{}`))
		}
		a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
		award, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, Actor: engineActor(a), Policy: store.SourceNone})
		if err != nil {
			t.Fatal(err)
		}
		host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}, "run", "build")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = host.Close() })
		principal, err := host.Principal(t.Context())
		if err != nil || principal.Capability.Prefix != award.Node.CapabilityPrefix {
			t.Fatalf("node authentication: %v", err)
		}
		snapshot, err := host.ReadSnapshot(t.Context(), "run")
		if err != nil || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].Ref.NodeID != "build" {
			t.Fatalf("node snapshot disclosed siblings: %+v %v", snapshot.Nodes, err)
		}
		actor := store.Actor{Kind: store.ActorHolder, CapabilityPrefix: award.Node.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Generation: award.Node.Generation}}
		if _, err := host.RenewNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)}, actor, false); (err == nil) != (run == "run" && node == "build") {
			t.Fatalf("renewed outside node award: %v", err)
		}
		actor.Generation++
		if _, err := host.RenewNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "build"}, actor, true); err == nil {
			t.Fatal("stale node actor renewed")
		}
		_, err = host.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)})
		if (err == nil) != (run == "run" && node == "build") {
			t.Fatalf("node scope run=%q node=%q: %v", run, node, err)
		}
		_, err = host.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte("foreign-bearer"))}, capability.RequestEvidence{Target: capability.Target{RunID: "run", NodeID: "build"}})
		if err == nil {
			t.Fatal("node read another credential's authority")
		}
		if err := host.CheckBinding(t.Context(), owner, capability.BindOwnNode, capability.Target{RunID: "run", NodeID: "build"}); err == nil {
			t.Fatal("node selected the owner's authority")
		}
		actor.Generation = award.Node.Generation
		engineAcceptedFixturePlan(t, server.engine, "a", "run", "build", "sibling")
		if result, err := host.MayStart(t.Context(), store.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(award.Node.Generation)}, actor); err != nil || result.Kind != store.AcquireGranted {
			t.Fatalf("execution admission: %+v, %v", result, err)
		}
		_, err = host.TransitionNode(t.Context(), store.StartNode{Ref: controllerwire.NodeRef{RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)}, Actor: actor, SourceAttempt: (*runspec.AttemptOrdinal)(&sourceAttempt)})
		if (err == nil) != (run == "run" && node == "build" && sourceAttempt == 0) {
			t.Fatalf("node transition escaped exact run/node/attempt fence: %v", err)
		}
	})
}
