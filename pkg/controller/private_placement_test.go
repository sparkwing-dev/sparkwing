package controller

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPrivateHostSnapshotCarriesCanonicalPlacementAcrossTransport(t *testing.T) {
	server, host, _, owner := localHostFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,spec_hash,execution_json) VALUES ('a','run','build','pending',?,?,?,?)`, time.Now().UnixNano(), []byte(`{"id":"build"}`), "declaration", []byte(`{}`))
	snapshot, err := host.ReadSnapshot(t.Context(), "run")
	if err != nil || len(snapshot.Nodes) != 1 || !store.OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("private placement snapshot: %v", err)
	}
	server.placement.defaultPrefers, server.placement.hold, server.placement.liveness = []string{"name=preferred"}, time.Hour, time.Minute
	engineHTTPRoot(t, &EngineController{Store: server.store}, "a", "preferred", []capability.Grant{capability.NodesClaim})
	if err := server.store.RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "preferred", Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = host.ReadSnapshot(t.Context(), "run")
	if err != nil || store.OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("controller placement policy did not reach private snapshot: %v", err)
	}
}
