package nodehost

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type recoveryGraph struct {
	controllerwire.Controller
	snapshot controllerwire.ScheduleSnapshot
}

func (g recoveryGraph) ReadSnapshot(context.Context, string) (controllerwire.ScheduleSnapshot, error) {
	return g.snapshot, nil
}

func TestRecoveryDependencies(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"own_dependencies", "failed_own_dependency", "parent_cancelled", "parent_succeeded", "group_expected", "group_running", "group_failed", "group_empty", "group_succeeded", "optional_recovery_failed"} {
		t.Run(mode, func(t *testing.T) {
			job := func(id, status, outcome string) controllerwire.ScheduleJob {
				return controllerwire.ScheduleJob{JobDetail: controllerwire.JobDetail{ID: id, Status: status, Outcome: outcome}, Declaration: nodeproto.JobSpec{ID: id}}
			}
			wire := recoveryGraph{snapshot: controllerwire.ScheduleSnapshot{RunID: "run", Jobs: []controllerwire.ScheduleJob{job("parent", "done", "failed"), job("prepare", "done", "success"), job("recovery", "pending", "")}}}
			parent, prerequisite, recovery := &wire.snapshot.Jobs[0], &wire.snapshot.Jobs[1], &wire.snapshot.Jobs[2]
			recovery.Declaration.OnFailureOf, recovery.Declaration.Needs = "parent", []string{"prepare"}
			kind, outcome, reason := store.DecisionReady, "", ""
			switch mode {
			case "own_dependencies":
				prerequisite.Status, prerequisite.Outcome = "running", ""
				kind = ""
			case "failed_own_dependency":
				prerequisite.Outcome = "failed"
				kind, outcome, reason = store.DecisionTerminal, "cancelled", "upstream_failed"
			case "parent_cancelled", "parent_succeeded":
				parent.Outcome = "cancelled"
				if mode == "parent_succeeded" {
					parent.Outcome = "success"
				}
				kind, outcome, reason = store.DecisionTerminal, "skipped", "parent_did_not_fail"
			case "group_expected", "group_running", "group_failed", "group_empty", "group_succeeded":
				recovery.Declaration.NeedsGenerated = []string{"generated"}
				group := controllerwire.ScheduleGenerator{ID: "generated", SourceJob: "prepare", State: "accepted"}
				if mode == "group_expected" {
					group.State, prerequisite.Expansion, kind = "expected", "expected", ""
				} else if mode != "group_empty" {
					group.Members = []string{"member"}
					member := job("member", "done", "success")
					if mode == "group_running" {
						member.Status, member.Outcome, kind = "running", "", ""
					} else if mode == "group_failed" {
						member.Outcome = "failed"
						kind, outcome, reason = store.DecisionTerminal, "cancelled", "upstream_failed"
					}
					wire.snapshot.Jobs = append(wire.snapshot.Jobs, member)
				}
				wire.snapshot.Generators = []controllerwire.ScheduleGenerator{group}
			case "optional_recovery_failed":
				parent.Declaration.Optional = true
				recovery.Status, recovery.Outcome, kind = "done", "failed", ""
			}
			graph, err := (&RemoteOwnerStore{Controller: wire}).Schedule(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			decision := store.Decide(graph)
			var selected store.NodeDecision
			for _, action := range decision.Nodes {
				if action.NodeID == "recovery" {
					selected = action
				}
			}
			if selected.Kind != kind || selected.Outcome != outcome || selected.Reason != reason {
				t.Fatalf("recovery decision=%+v, want %s %s %s", selected, kind, outcome, reason)
			}
			if mode == "optional_recovery_failed" && decision.Outcome != "success" {
				t.Fatalf("optional source and its recovery decided the run: %+v", decision)
			}
		})
	}
}
