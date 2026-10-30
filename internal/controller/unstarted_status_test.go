package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRunDetailExplainsJobsThatNeverStarted(t *testing.T) {
	for _, tc := range []struct{ reason, outcome, explanation string }{
		{"queue_timeout", "failed", "waiting to start"},
		{"upstream_failed", "cancelled", "b"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			server, _ := engineRuntimeStore(t)
			owner := engineRuntimeClaim(t, server, capability.UnitNode)
			engineSQL(t, server.Store, `UPDATE nodes SET claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',status='done',outcome=?,failure_reason=?,attempt=0,finished_at=1,spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, tc.outcome, tc.reason, []byte(`{"id":"job","deps":["b","c"]}`))
			engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES ('a','run','b','done','failed',1,?,?),('a','run','c','done','success',1,?,?)`, []byte(`{"id":"b"}`), []byte(`{}`), []byte(`{"id":"c"}`), []byte(`{}`))
			detail, err := server.RunDetail(t.Context(), owner)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Jobs) != 3 {
				t.Fatalf("jobs=%d, want three", len(detail.Jobs))
			}
			for _, job := range detail.Jobs {
				if job.ID == "job" && (job.Attempt != 0 || !strings.Contains(job.StatusDetail, tc.reason) || !strings.Contains(job.StatusDetail, tc.explanation)) {
					t.Fatalf("unstarted job has no explanation: %+v", job)
				}
			}
		})
	}
}

func TestRunDetailNamesOnlyBlockingDependencies(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitNode)
	engineSQL(t, server.Store, `UPDATE nodes SET claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',status='done',outcome='cancelled',failure_reason='upstream_failed',attempt=0,finished_at=1,spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","deps":["hard","soft","skip","memo","recovery"],"on_failure_of":"recovery"}`))
	for _, dependency := range []struct{ id, outcome, spec string }{
		{"hard", "failed", `{"id":"hard"}`},
		{"soft", "failed", `{"id":"soft","continue_on_error":true}`},
		{"skip", "skipped", `{"id":"skip"}`},
		{"memo", "memoized", `{"id":"memo"}`},
		{"recovery", "failed", `{"id":"recovery"}`},
	} {
		engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES ('a','run',?,'done',?,1,?,?)`, dependency.id, dependency.outcome, []byte(dependency.spec), []byte(`{}`))
	}
	detail, err := server.RunDetail(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range detail.Jobs {
		if job.ID == "job" && job.StatusDetail != `upstream_failed: blocked by "hard" (failed)` {
			t.Fatalf("blocking dependency detail: %s", job.StatusDetail)
		}
	}
}

func TestRunDetailNamesFailedGeneratedDependency(t *testing.T) {
	source := nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"children"}}}
	consumer := nodeproto.JobSpec{ID: "consumer", NeedsGenerated: []string{"children"}}
	failed := nodeproto.JobSpec{ID: "failed-member", Groups: []string{"children"}}
	soft := nodeproto.JobSpec{ID: "soft-member", Groups: []string{"children"}, ContinueOnError: true}
	plan, _ := json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{source, consumer}})
	expansion, _ := json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{failed, soft}})
	finished := time.Unix(1, 0)
	snapshot := store.RunSnapshot{Run: store.RunRow{Team: "a", Execution: runstate.Execution{ID: "run", Number: 1, State: runstate.RunRunning, Plan: plan, Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Team: "a"}}}}, StoreNow: finished, Expansions: []runstate.AcceptedExpansionRow{{Source: runspec.NodeRef{RunID: "run", NodeID: "source"}, Body: expansion}}}
	for _, job := range []nodeproto.JobSpec{source, consumer, failed, soft} {
		spec, _ := json.Marshal(job)
		row := runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(job.ID)}, Spec: spec, Status: runstate.StatusDone, Outcome: runstate.NodeFailed, FinishedAt: &finished}
		if job.ID == "source" {
			row.Outcome, row.Expansion = runstate.NodeSuccess, runstate.ExpansionAccepted
		}
		if job.ID == "consumer" {
			row.Outcome, row.FailureReason = runstate.NodeCancelled, "upstream_failed"
		}
		snapshot.Nodes = append(snapshot.Nodes, row)
	}
	if _, err := snapshot.Scheduling(); err != nil {
		t.Fatal(err)
	}
	detail, err := runDetailResponse(capability.Target{Team: "a", RunID: "run"}, RunStatusFacts{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range detail.Jobs {
		if job.ID == "consumer" && job.StatusDetail != `upstream_failed: blocked by "failed-member" (failed)` {
			t.Fatalf("generated dependency detail: %s", job.StatusDetail)
		}
	}
}
