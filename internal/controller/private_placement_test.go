package controller

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestPrivateHostNodeReadsStateWithoutPlacement(t *testing.T) {
	server, host, _, _ := localHostFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,spec_hash,execution_json) VALUES ('a','run','build','pending',?,?,?,?)`, time.Now().UnixNano(), []byte(`{"id":"build"}`), "declaration", []byte(`{}`))
	placementReads := 0
	server.engine.claimProfiles = engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, error) {
		placementReads++
		return match.Profile{}, nil
	})
	node, err := host.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "build"})
	if err != nil || node.Ref.NodeID != "build" {
		t.Fatalf("private node state: %+v %v", node, err)
	}
	if placementReads != 0 || node.OwnerExecution != nil {
		t.Fatalf("node state read projected placement: reads=%d decision=%+v", placementReads, node.OwnerExecution)
	}
}

func TestPrivateHostSnapshotCarriesCanonicalPlacementAcrossTransport(t *testing.T) {
	server, host, _, owner := localHostFixture(t)
	engineSQL(t, server.store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,spec_hash,execution_json) VALUES ('a','run','build','pending',?,?,?,?)`, time.Now().UnixNano(), []byte(`{"id":"build"}`), "declaration", []byte(`{}`))
	snapshot, err := host.ReadSnapshot(t.Context(), "run")
	if err != nil || len(snapshot.Nodes) != 1 || !store.OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("private placement snapshot: %v", err)
	}
	server.placement.defaultPrefers, server.placement.hold, server.placement.liveness = []string{"name=preferred"}, time.Hour, time.Minute
	engineHTTPRoot(t, newEngineFixture(server.store, EngineController{}), "a", "preferred", []capability.Grant{capability.NodesClaim})
	if err := server.store.AsOperator().RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "preferred"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = host.ReadSnapshot(t.Context(), "run")
	if err != nil || store.OwnerCanExecute(owner)(snapshot.Nodes[0]) {
		t.Fatalf("controller placement policy did not reach private snapshot: %v", err)
	}
}
