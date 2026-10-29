package controller

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
