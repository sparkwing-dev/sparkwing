package controller

import (
	"context"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestRelinquishedAwardIsRefusedOutsideNodeState(t *testing.T) {
	server, _, tenant, owner, _ := localHostAwardFixture(t)
	acceptCapacityDocument(t, server, tenant, owner, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "build", Steps: []nodewire.StepSpec{{ID: "body"}}}}})
	ownerActor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	ref := runspec.NodeRef{RunID: "run", NodeID: "build"}
	if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: ownerActor}); err != nil {
		t.Fatal(err)
	}
	award, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: ownerActor, Policy: runstate.SourceNone})
	if err != nil || award.Kind != runstate.Execute {
		t.Fatalf("award=%+v error=%v", award, err)
	}
	bearer := string(award.Bearer)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/v1/runs/run/jobs/build", ""},
		{"GET", "/api/v1/runs/run/local-host/nodes/build", ""},
		{"POST", "/api/v1/runs/run/jobs/build/heartbeat", "{}"},
		{"GET", "/api/v1/secrets/TOKEN?run=run", ""},
		{"POST", "/api/v1/data/has-many", "{}"},
	}
	for _, route := range routes {
		if reply := sourceUploadHTTP(server.Handler(), bearer, route.method, route.path, route.body); reply.Code == http.StatusUnauthorized {
			t.Fatalf("live award failed authentication on %s %s: %s", route.method, route.path, reply.Body)
		}
	}
	nodeHost, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return bearer, nil }}, "run", "build")
	if err != nil {
		t.Fatal(err)
	}
	defer nodeHost.Close()
	holder := runstate.Actor{Kind: runstate.ActorHolder, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "build", Holder: award.Node.ClaimedBy, Generation: award.Node.Generation}, CapabilityPrefix: award.Node.CapabilityPrefix}
	if err := nodeHost.RelinquishStart(t.Context(), runstate.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(award.Node.Generation)}, holder); err != nil {
		t.Fatal(err)
	}
	if row, err := tenant.ReadNode(t.Context(), ref); err != nil || row.ClaimedBy != "" || row.ReadyAt == nil {
		t.Fatalf("relinquish did not return the job: %+v %v", row, err)
	}
	for _, route := range routes {
		if reply := sourceUploadHTTP(server.Handler(), bearer, route.method, route.path, route.body); reply.Code != http.StatusUnauthorized {
			t.Errorf("relinquished award on %s %s: HTTP %d %s", route.method, route.path, reply.Code, reply.Body)
		}
	}
}
