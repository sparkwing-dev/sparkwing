package store

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzDeclaredPipelineReferenceDoesNotEscapeItsJob(f *testing.F) {
	for change := uint8(0); change < 9; change++ {
		f.Add(change, "suffix")
	}
	f.Fuzz(func(t *testing.T, change uint8, suffix string) {
		s, _, actor, _ := resultFixture(t)
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run'`, []byte(`{"id":"job","pipeline_refs":[{"pipeline":"producer","job":"output"}]}`))
		sealed, err := json.Marshal(runspec.SealedRequest{Version: 1, Request: runspec.Request{Pipeline: "producer"}})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES ('a','producer-run',2,'producer','success',?,'producer',1,'gh:1','host','execute',?,'account',1,2,'r30',30)`, sealed, []byte(`[]`))
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES ('a','producer-run','output','done','success',2,?,?)`, []byte(`{"id":"output"}`), []byte(`{}`))
		p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
		if err != nil {
			t.Fatal(err)
		}
		target := capability.Target{Team: "a", RunID: "producer-run", NodeID: "output"}
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, target); err != nil {
			t.Fatalf("declared reference: %v", err)
		}
		switch change % 9 {
		case 0:
			target.NodeID += "-" + suffix
		case 1:
			target.RunID += "-" + suffix
		case 2:
			target.Team = "b"
		case 3:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run'`, []byte(`{"id":"job","pipeline_refs":[{"pipeline":"producer","job":"other"}]}`))
		case 4:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run'`, []byte(`{"id":"job","pipeline_refs":[{"pipeline":"other","job":"output"}]}`))
		case 5:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=8 WHERE team='a' AND run_id='run'`)
		case 6:
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
		case 7:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET outcome='failed' WHERE team='a' AND run_id='producer-run'`)
		case 8:
			rewriteSchemaExec(t, s.db, `UPDATE runs SET status='running',finished_at=NULL WHERE team='a' AND id='producer-run'`)
		}
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, target); err == nil {
			t.Fatalf("changed reference %d read other output", change%9)
		}
	})
}
