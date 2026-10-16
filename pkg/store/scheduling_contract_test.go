package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestRecoveryDependencies(t *testing.T) {
	for _, test := range []struct {
		name, dependencyStatus, dependencyOutcome, kind, outcome string
	}{
		{"own_dependencies", "running", "", "", ""},
		{"parent_failure", "done", "success", "ready", ""},
		{"failed_own_dependency", "done", "failed", "terminal", "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
				{ID: "deploy", Status: "done", Outcome: "failed"},
				{ID: "independent", Status: test.dependencyStatus, Outcome: test.dependencyOutcome},
				{ID: "rollback", Status: "pending", Generation: 3, OnFailureOf: "deploy", Deps: []string{"deploy", "independent"}},
			}}
			decision := Decide(snapshot)
			if test.kind == "" {
				if len(decision.Nodes) != 0 || decision.Outcome != "" {
					t.Fatalf("recovery must wait: %+v", decision)
				}
				return
			}
			if len(decision.Nodes) != 1 || decision.Nodes[0].NodeID != "rollback" || string(decision.Nodes[0].Kind) != test.kind || decision.Nodes[0].Outcome != test.outcome || decision.Nodes[0].Generation != 3 {
				t.Fatalf("recovery decision = %+v", decision)
			}
			if test.outcome == "cancelled" && decision.Nodes[0].Reason != "upstream_failed" {
				t.Fatalf("recovery reason = %q", decision.Nodes[0].Reason)
			}
			snapshot.Nodes[1].Status, snapshot.Nodes[1].Outcome = "done", "success"
			snapshot.Nodes[2].Status, snapshot.Nodes[2].Outcome = "done", "success"
			if got := Decide(snapshot).Outcome; got != "failed" {
				t.Fatalf("successful recovery erased parent failure: %q", got)
			}
		})
	}
}

func TestSchedulingDependencyVerdicts(t *testing.T) {
	snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
		{ID: "after-after", Status: "pending", Deps: []string{"after-hard"}},
		{ID: "after-hard", Status: "pending", Deps: []string{"hard"}},
		{ID: "hard", Status: "done", Outcome: "failed"},
		{ID: "soft", Status: "done", Outcome: "failed", ContinueOnError: true},
		{ID: "after-soft", Status: "pending", Deps: []string{"soft"}},
		{ID: "approval", Kind: "approval", Status: "pending", DepsIfPresent: []string{"absent"}},
		{ID: "unneeded", Status: "pending", OnFailureOf: "ok"},
		{ID: "ok", Status: "done", Outcome: "success"},
	}}
	before, _ := json.Marshal(snapshot)
	got := Decide(snapshot)
	want := map[string]NodeDecision{
		"after-hard":  {NodeID: "after-hard", Kind: DecisionTerminal, Outcome: "cancelled", Reason: "upstream_failed"},
		"after-after": {NodeID: "after-after", Kind: DecisionTerminal, Outcome: "cancelled", Reason: "upstream_failed"},
		"after-soft":  {NodeID: "after-soft", Kind: DecisionReady},
		"approval":    {NodeID: "approval", Kind: DecisionApproval},
	}
	for _, decision := range got.Nodes {
		if decision.NodeID == "unneeded" {
			if decision.Kind != DecisionTerminal || decision.Outcome != "skipped" {
				t.Fatalf("nonmatching recovery = %+v", decision)
			}
			continue
		}
		if expected, ok := want[decision.NodeID]; !ok || decision != expected {
			t.Fatalf("decision = %+v; expected %+v", decision, expected)
		}
		delete(want, decision.NodeID)
	}
	if len(want) != 0 || len(got.Nodes) != 5 || got.Outcome != "" {
		t.Fatalf("incomplete decisions: %+v", got)
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("Decide mutated its input")
	}
}

func TestSchedulingOptionalAndRecoveryDoNotFailRun(t *testing.T) {
	snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
		{ID: "notify", Status: "done", Outcome: "failed", Optional: true},
		{ID: "recover", Status: "done", Outcome: "failed", OnFailureOf: "notify"},
	}}
	if got := Decide(snapshot).Outcome; got != "success" {
		t.Fatalf("optional parent and recovery verdict = %q", got)
	}
}

func TestSchedulingCancellationDrainsHolders(t *testing.T) {
	snapshot := ScheduleSnapshot{CancelRequested: true, Nodes: []ScheduleNode{
		{ID: "waiting", Status: "pending"},
		{ID: "approval", Status: "approval_pending"},
		{ID: "claimed", Status: "pending", Claimed: true, Generation: 4},
		{ID: "running", Status: "running", Claimed: true, Generation: 5},
	}}
	decision := Decide(snapshot)
	if len(decision.Nodes) != 4 || decision.Outcome != "" {
		t.Fatalf("cancellation must drain: %+v", decision)
	}
	for index, node := range decision.Nodes {
		want := DecisionTerminal
		if index >= 2 {
			want = DecisionCancelHolder
		}
		if node.Kind != want || node.Outcome != "cancelled" || node.Generation != snapshot.Nodes[index].Generation {
			t.Fatalf("cancellation decision = %+v", node)
		}
		snapshot.Nodes[index].Status, snapshot.Nodes[index].Outcome, snapshot.Nodes[index].Claimed = "done", "cancelled", false
	}
	if got := Decide(snapshot).Outcome; got != "cancelled" {
		t.Fatalf("drained cancellation = %q", got)
	}
}

func TestSchedulingGeneratorBarrier(t *testing.T) {
	for _, outcome := range []string{"success", "memoized"} {
		t.Run(outcome, func(t *testing.T) {
			snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{{ID: "source", Status: "done", Outcome: outcome, Generation: 2, Expansion: "expected"}}, Expansions: []ScheduleExpansion{{GeneratorID: "generate", SourceNodeID: "source", State: "expected"}}}
			decision := Decide(snapshot)
			if decision.Outcome != "" || len(decision.Nodes) != 1 || decision.Nodes[0] != (NodeDecision{NodeID: "source", Generation: 2, GeneratorID: "generate", Kind: DecisionExpand}) {
				t.Fatalf("expected expansion must hold settlement: %+v", decision)
			}
			snapshot.Nodes[0].Expansion, snapshot.Expansions[0].State = "accepted", "accepted"
			if got := Decide(snapshot).Outcome; got != "success" {
				t.Fatalf("empty accepted expansion = %q", got)
			}
		})
	}
	for _, test := range []struct{ state, sourceOutcome, outcome string }{
		{"expected", "success", ""},
		{"failed", "success", "cancelled"},
		{"none", "failed", "cancelled"},
		{"none", "skipped", "skipped"},
		{"accepted", "success", ""},
	} {
		t.Run(test.state+"_"+test.sourceOutcome, func(t *testing.T) {
			snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
				{ID: "source", Status: "done", Outcome: test.sourceOutcome, Expansion: test.state},
				{ID: "dependent", Status: "pending", NeedsGenerated: []string{"generate"}},
			}, Expansions: []ScheduleExpansion{{GeneratorID: "generate", SourceNodeID: "source", State: test.state}}}
			decision := Decide(snapshot)
			for _, node := range decision.Nodes {
				if node.NodeID == "dependent" {
					if node.Outcome != test.outcome || test.state == "expected" {
						t.Fatalf("generated dependency = %+v", decision)
					}
					return
				}
			}
			if test.state != "expected" {
				t.Fatalf("missing generated dependency decision: %+v", decision)
			}
		})
	}
}

func TestSchedulingRecoveryWaitsForGeneratedGroup(t *testing.T) {
	snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
		{ID: "parent", Status: "done", Outcome: "failed"},
		{ID: "source", Status: "done", Outcome: "success", Expansion: "accepted"},
		{ID: "member", Status: "running", Claimed: true},
		{ID: "recovery", Status: "pending", OnFailureOf: "parent", NeedsGenerated: []string{"group"}},
	}, Expansions: []ScheduleExpansion{{GeneratorID: "group", SourceNodeID: "source", State: "accepted", Members: []string{"member"}}}}
	if got := Decide(snapshot); len(got.Nodes) != 0 || got.Outcome != "" {
		t.Fatalf("recovery ran before generated member: %+v", got)
	}
	snapshot.Nodes[2].Status, snapshot.Nodes[2].Outcome, snapshot.Nodes[2].Claimed = "done", "failed", false
	got := Decide(snapshot)
	if len(got.Nodes) != 1 || got.Nodes[0].NodeID != "recovery" || got.Nodes[0].Outcome != "cancelled" {
		t.Fatalf("recovery ignored failed generated member: %+v", got)
	}
}

func scheduleFixture(t *testing.T, jobs ...nodeproto.JobSpec) RunSnapshot {
	t.Helper()
	plan, err := json.Marshal(nodeproto.PlanDoc{Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{Run: RunRow{ID: "run", Plan: plan}}
	for _, job := range jobs {
		spec, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		row := NodeRow{Ref: NodeRef{RunID: "run", NodeID: job.ID}, Status: StatusPending, Spec: spec}
		if len(job.Closures.Generate) != 0 {
			row.Expansion = ExpansionExpected
		}
		snapshot.Nodes = append(snapshot.Nodes, row)
	}
	return snapshot
}

func TestSchedulingProjectionRetainsDeclarations(t *testing.T) {
	snapshot := scheduleFixture(t,
		nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"empty", "members"}}},
		nodeproto.JobSpec{ID: "after", NeedsGenerated: []string{"empty", "members"}},
	)
	snapshot.Nodes[0].Status, snapshot.Nodes[0].Outcome, snapshot.Nodes[0].Expansion = StatusDone, NodeSuccess, ExpansionAccepted
	snapshot.Nodes[0].Generation = 7
	child := scheduleFixture(t, nodeproto.JobSpec{ID: "child", Needs: []string{"source"}, Groups: []string{"members"}})
	snapshot.Nodes = append(snapshot.Nodes, child.Nodes[0])
	snapshot.Expansions = []AcceptedExpansionRow{{Source: snapshot.Nodes[0].Ref, SourceGeneration: 7, Body: child.Run.Plan}}
	snapshot.Nodes[0], snapshot.Nodes[2] = snapshot.Nodes[2], snapshot.Nodes[0]
	got, err := snapshot.Scheduling()
	if err != nil {
		t.Fatal(err)
	}
	if got.Nodes[0].ID != "source" || got.Nodes[1].ID != "after" || got.Nodes[2].ID != "child" {
		t.Fatalf("projection used live row order: %+v", got.Nodes)
	}
	want := []ScheduleExpansion{{GeneratorID: "empty", SourceNodeID: "source", State: "accepted"}, {GeneratorID: "members", SourceNodeID: "source", State: "accepted", Members: []string{"child"}}}
	if !reflect.DeepEqual(got.Expansions, want) {
		t.Fatalf("expansions = %+v; want %+v", got.Expansions, want)
	}
}

func TestSchedulingProjectionRejectsInconsistentState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RunSnapshot)
	}{
		{"missing_plan", func(s *RunSnapshot) { s.Run.Plan = nil }},
		{"unknown_plan_field", func(s *RunSnapshot) { s.Run.Plan = []byte(`{"jobs":[{"id":"one"}],"state":"running"}`) }},
		{"missing_row", func(s *RunSnapshot) { s.Nodes = nil }},
		{"undeclared_row", func(s *RunSnapshot) { s.Nodes = append(s.Nodes, NodeRow{Ref: NodeRef{RunID: "run", NodeID: "extra"}}) }},
		{"duplicate_row", func(s *RunSnapshot) { s.Nodes = append(s.Nodes, s.Nodes[0]) }},
		{"foreign_run", func(s *RunSnapshot) { s.Nodes[0].Ref.RunID = "other" }},
		{"missing_spec", func(s *RunSnapshot) { s.Nodes[0].Spec = nil }},
		{"changed_spec", func(s *RunSnapshot) { s.Nodes[0].Spec = []byte(`{"id":"one","deps":["forged"]}`) }},
		{"invalid_state", func(s *RunSnapshot) { s.Nodes[0].Outcome = NodeSuccess }},
		{"terminal_claim", func(s *RunSnapshot) {
			s.Nodes[0].Status, s.Nodes[0].Outcome, s.Nodes[0].ClaimedBy = StatusDone, NodeSuccess, "holder"
		}},
		{"undeclared_generator", func(s *RunSnapshot) { s.Nodes[0].Expansion = ExpansionExpected }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := scheduleFixture(t, nodeproto.JobSpec{ID: "one"})
			test.mutate(&snapshot)
			if _, err := snapshot.Scheduling(); err == nil {
				t.Fatal("inconsistent projection succeeded")
			}
		})
	}
}

func TestSchedulingProjectionDoesNotReapElapsedClaims(t *testing.T) {
	snapshot := scheduleFixture(t, nodeproto.JobSpec{ID: "one"})
	expired := time.Unix(1, 0)
	snapshot.StoreNow = time.Unix(2, 0)
	snapshot.Nodes[0].ClaimedBy, snapshot.Nodes[0].LeaseExpiresAt = "holder", &expired
	got, err := snapshot.Scheduling()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Nodes[0].Claimed || len(Decide(got).Nodes) != 0 {
		t.Fatalf("elapsed claim was requeued: %+v", got)
	}
}

func TestSchedulingProjectionRejectsInvalidExpansions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RunSnapshot)
	}{
		{"missing_body", func(s *RunSnapshot) { s.Expansions = nil }},
		{"foreign_run", func(s *RunSnapshot) { s.Expansions[0].Source.RunID = "foreign" }},
		{"wrong_generation", func(s *RunSnapshot) { s.Expansions[0].SourceGeneration++ }},
		{"duplicate_source", func(s *RunSnapshot) { s.Expansions = append(s.Expansions, s.Expansions[0]) }},
		{"missing_jobs", func(s *RunSnapshot) { s.Expansions[0].Body = []byte(`{}`) }},
		{"null_jobs", func(s *RunSnapshot) { s.Expansions[0].Body = []byte(`{"jobs":null}`) }},
		{"source_pending", func(s *RunSnapshot) { s.Nodes[0].Status, s.Nodes[0].Outcome = StatusPending, "" }},
		{"source_failed", func(s *RunSnapshot) { s.Nodes[0].Outcome = NodeFailed }},
		{"undeclared_group", func(s *RunSnapshot) { s.Expansions[0].Body = []byte(`{"jobs":[{"id":"child","groups":["unknown"]}]}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := scheduleFixture(t, nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"group"}}})
			snapshot.Nodes[0].Status, snapshot.Nodes[0].Outcome, snapshot.Nodes[0].Expansion = StatusDone, NodeSuccess, ExpansionAccepted
			snapshot.Expansions = []AcceptedExpansionRow{{Source: snapshot.Nodes[0].Ref, Body: []byte(`{"jobs":[]}`)}}
			if _, err := snapshot.Scheduling(); err != nil {
				t.Fatal(err)
			}
			test.mutate(&snapshot)
			if _, err := snapshot.Scheduling(); err == nil {
				t.Fatal("invalid expansion projected")
			}
		})
	}
}

func TestSchedulingProjectionRejectsInvalidGraphs(t *testing.T) {
	for _, test := range []struct {
		name string
		jobs []nodeproto.JobSpec
	}{
		{"undeclared_dep", []nodeproto.JobSpec{{ID: "job", Needs: []string{"absent"}}}},
		{"undeclared_recovery_parent", []nodeproto.JobSpec{{ID: "job", OnFailureOf: "absent"}}},
		{"undeclared_generator", []nodeproto.JobSpec{{ID: "job", NeedsGenerated: []string{"absent"}}}},
		{"cycle", []nodeproto.JobSpec{{ID: "one", Needs: []string{"two"}}, {ID: "two", Needs: []string{"one"}}}},
		{"recovery_cycle", []nodeproto.JobSpec{{ID: "one", OnFailureOf: "two"}, {ID: "two", Needs: []string{"one"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scheduleFixture(t, test.jobs...).Scheduling(); err == nil {
				t.Fatal("invalid graph projected")
			}
		})
	}
}

func TestSchedulingTerminalCascadeResolvesGenerator(t *testing.T) {
	snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
		{ID: "failed", Status: "done", Outcome: "failed"},
		{ID: "generator", Status: "pending", Deps: []string{"failed"}, Expansion: "expected"},
		{ID: "after", Status: "pending", NeedsGenerated: []string{"group"}},
	}, Expansions: []ScheduleExpansion{{GeneratorID: "group", SourceNodeID: "generator", State: "expected"}}}
	got := Decide(snapshot)
	if len(got.Nodes) != 2 || got.Outcome != "failed" {
		t.Fatalf("terminal cascade failed to resolve expected generator: %+v", got)
	}
	for _, node := range got.Nodes {
		if node.Kind != DecisionTerminal || node.Outcome != "cancelled" || node.Reason != "upstream_failed" {
			t.Fatalf("cascade decision = %+v", node)
		}
	}
}

func TestSchedulingProjectionNestedExpansion(t *testing.T) {
	snapshot := scheduleFixture(t, nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"first"}}})
	child := scheduleFixture(t, nodeproto.JobSpec{ID: "child", Groups: []string{"first"}, Needs: []string{"source"}, Closures: nodeproto.ClosureSpec{Generate: []string{"second"}}})
	grandchild := scheduleFixture(t, nodeproto.JobSpec{ID: "grandchild", Groups: []string{"second"}, Needs: []string{"child"}})
	snapshot.Nodes[0].Status, snapshot.Nodes[0].Outcome, snapshot.Nodes[0].Expansion = StatusDone, NodeSuccess, ExpansionAccepted
	child.Nodes[0].Status, child.Nodes[0].Outcome, child.Nodes[0].Expansion = StatusDone, NodeMemoized, ExpansionAccepted
	snapshot.Nodes = append(snapshot.Nodes, child.Nodes[0], grandchild.Nodes[0])
	snapshot.Expansions = []AcceptedExpansionRow{{Source: snapshot.Nodes[0].Ref, Body: child.Run.Plan}, {Source: child.Nodes[0].Ref, Body: grandchild.Run.Plan}}
	got, err := snapshot.Scheduling()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Expansions) != 2 || !reflect.DeepEqual(got.Expansions[1].Members, []string{"grandchild"}) {
		t.Fatalf("nested expansion = %+v", got)
	}
	decision := Decide(got)
	if len(decision.Nodes) != 1 || decision.Nodes[0].NodeID != "grandchild" || decision.Nodes[0].Kind != DecisionReady {
		t.Fatalf("nested scheduling = %+v", decision)
	}
}

func TestSchedulingMissingGeneratedWorkCannotContinue(t *testing.T) {
	snapshot := ScheduleSnapshot{Nodes: []ScheduleNode{
		{ID: "source", Status: "done", Outcome: "failed", ContinueOnError: true, Expansion: "none"},
		{ID: "after", Status: "pending", NeedsGenerated: []string{"group"}},
	}, Expansions: []ScheduleExpansion{{GeneratorID: "group", SourceNodeID: "source", State: "none"}}}
	decision := Decide(snapshot)
	if len(decision.Nodes) != 1 || decision.Nodes[0].Outcome != "cancelled" {
		t.Fatalf("nonexistent generated work released downstream: %+v", decision)
	}
}

func TestSchedulingProjectionAllowsStaticGroups(t *testing.T) {
	snapshot := scheduleFixture(t,
		nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"fanout"}}},
		nodeproto.JobSpec{ID: "static", Groups: []string{"fanout", "display"}, Needs: []string{"source"}},
	)
	got, err := snapshot.Scheduling()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 2 || len(got.Expansions) != 1 || len(got.Expansions[0].Members) != 0 {
		t.Fatalf("static groups changed generated membership: %+v", got)
	}
}
