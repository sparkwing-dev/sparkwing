package controller

import (
	"context"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPrivateNodeRecoveryReadsOnlyItsPersistedSource(t *testing.T) {
	server, _, tenant, owner := localHostFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,finished_at,spec_json,execution_json,failure_json) VALUES ('a','run','source','done','failed',4,1,?,?,?)`, []byte(`{"id":"source"}`), []byte(`{}`), []byte(`{"job":"source","stage":"verify","error":{"code":"sdk_error","message":"recorded source failure"}}`))
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','recovery','pending',1,?,?)`, []byte(`{"id":"recovery","on_failure_of":"source"}`), []byte(`{}`))
	a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	award, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: runspec.NodeRef{RunID: "run", NodeID: "recovery"}, Actor: engineActor(a), Policy: runstate.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}
	host, err := client.NewNodeHostClient(options, "run", "recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	source := runspec.NodeRef{RunID: "run", NodeID: "source"}
	row, err := host.ReadNode(t.Context(), source)
	if err != nil || row.Ref != source || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeFailed || row.Generation != 4 {
		t.Fatalf("recovery source: %+v %v", row, err)
	}
	snapshot, err := host.ReadSnapshot(t.Context(), "run")
	if err != nil || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].Ref.NodeID != "recovery" {
		t.Fatalf("recovery source entered executable nodes: %+v %v", snapshot.Nodes, err)
	}
	controller, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodehost.NewRemoteHostStore(controller, host, "unit", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	cfg := nodehost.WithClient(nodehost.NativeConfig{Store: state}, host)
	if cfg.ReadFailure == nil {
		t.Fatal("native recovery has no failure reader")
	}
	failure, err := cfg.ReadFailure(t.Context(), source, int64(row.Generation))
	if err != nil || failure == nil || failure.Job != "source" || failure.Stage != "verify" || failure.Error == nil || failure.Error.Message != "recorded source failure" {
		t.Fatalf("persisted source failure: %+v %v", failure, err)
	}
	if _, err := cfg.ReadFailure(t.Context(), source, 5); err == nil {
		t.Fatal("recovery read a different source generation")
	}
}
