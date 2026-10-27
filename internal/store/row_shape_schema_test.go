package store_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestSchemaRejectsIncompleteLifecycleRows(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{"node holder only", `UPDATE nodes SET claimed_by='runner'`},
		{"node capability only", `UPDATE nodes SET capability_prefix='claim'`},
		{"node lease only", `UPDATE nodes SET lease_expires_at=2`},
		{"node empty holder", `UPDATE nodes SET claimed_by='',capability_prefix='claim',lease_expires_at=2,generation=1`},
		{"node unfenced claim", `UPDATE nodes SET claimed_by='runner',capability_prefix='claim',lease_expires_at=2`},
		{"running node without claim", `UPDATE nodes SET status='running'`},
		{"running node without attempt", `UPDATE nodes SET status='running',claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1,started_at=1`},
		{"running node without start", `UPDATE nodes SET status='running',claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1,attempt=1`},
		{"approval node with claim", `UPDATE nodes SET status='approval_pending',claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1`},
		{"negative resources", `UPDATE nodes SET requested_cores=-1`},
		{"fractional flag", `UPDATE nodes SET optional=2`},
		{"trigger without claim", `UPDATE triggers SET status='claimed'`},
		{"trigger holder only", `UPDATE triggers SET status='claimed',claimed_by='runner'`},
		{"trigger without claim time", `UPDATE triggers SET status='claimed',claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1`},
		{"pending trigger with claim", `UPDATE triggers SET claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1,claimed_at=1`},
		{"finished trigger without timestamp", `UPDATE triggers SET status='done'`},
		{"unfinished trigger with timestamp", `UPDATE triggers SET finished_at=2`},
		{"pending trigger with start", `UPDATE triggers SET started_at=1`},
		{"resolved approval without timestamp", `INSERT INTO approvals(team,run_id,node_id,requested_at,resolution) VALUES ('a','run','job',1,'approved')`},
		{"unresolved approval with timestamp", `INSERT INTO approvals(team,run_id,node_id,requested_at,resolved_at) VALUES ('a','run','job',1,2)`},
		{"approval missing node", `INSERT INTO approvals(team,run_id,node_id,requested_at) VALUES ('a','run','missing',1)`},
		{"claimed eval without claim", `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',0,'claimed')`},
		{"running eval without start", `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,claimed_by,capability_prefix,lease_expires_at,generation,claimed_at) VALUES ('a','run','job',0,'running','runner','claim',2,1,1)`},
		{"finished eval without timestamp", `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',0,'done')`},
		{"unfinished eval with timestamp", `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,finished_at) VALUES ('a','run','job',0,'pending',2)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := lifecycleSchemaFixture(t, false)
			if _, err := s.DB().ExecContext(t.Context(), test.query); err == nil || !strings.Contains(strings.ToLower(err.Error()), "constraint") {
				t.Fatalf("invalid lifecycle row: %v", err)
			}
		})
	}
}

func TestSchemaRejectsContradictoryAttemptAndStepRows(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{"second open attempt", `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',4,3,'runner','claim','runner','runner','remote',1)`},
		{"attempt empty holder", `UPDATE node_attempts SET holder=''`},
		{"attempt empty capability", `UPDATE node_attempts SET capability_prefix=''`},
		{"attempt backward finish", `UPDATE node_attempts SET outcome='failed',finished_at=started_at-1`},
		{"step no start", `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status) VALUES ('a','run','job',3,2,'compile','running')`},
		{"terminal step no finish", `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status,started_at) VALUES ('a','run','job',3,2,'compile','passed',1)`},
		{"running step with finish", `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status,started_at,finished_at) VALUES ('a','run','job',3,2,'compile','running',1,2)`},
		{"step backward finish", `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status,started_at,finished_at) VALUES ('a','run','job',3,2,'compile','passed',2,1)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := lifecycleSchemaFixture(t, true)
			if _, err := s.DB().ExecContext(t.Context(), test.query); err == nil || !strings.Contains(strings.ToLower(err.Error()), "constraint") {
				t.Fatalf("invalid attempt or step: %v", err)
			}
		})
	}
}

func lifecycleSchemaFixture(t *testing.T, running bool) *store.Store {
	t.Helper()
	s := storetest.Open(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, query), args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'p','running',?,'request',1,'gh:1','host','execute','account',1,'r30',30)`, []byte(`{}`))
	exec(`INSERT INTO triggers(team,id,unit_kind,available_at,created_at,needs_labels,prefers_labels) VALUES ('a','run','trigger',1,1,?,?)`, []byte(`[]`), []byte(`[]`))
	exec(`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, []byte(`{}`), []byte(`{}`))
	if running {
		exec(`UPDATE nodes SET status='running',claimed_by='runner',capability_prefix='claim',lease_expires_at=3,generation=2,attempt=3,started_at=1`)
		exec(`INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',3,2,'runner','claim','runner','runner','remote',1)`)
	}
	return s
}
