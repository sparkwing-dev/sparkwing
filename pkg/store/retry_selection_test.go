package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzRetryMaterializationUsesCommittedSelection(f *testing.F) {
	f.Add("failed", "source", "job", int64(1), true)
	f.Add("all", "source", "job", int64(1), true)
	f.Add("failed", "source", "job", int64(1), false)
	f.Add("failed", "source", "job", int64(2), true)
	f.Fuzz(func(t *testing.T, mode, run, job string, seq int64, selected bool) {
		if len(mode)+len(run)+len(job) > 256 {
			t.Skip()
		}
		tenant, request := materializationFixture(t, SourceManualRetry)
		selection := &RetrySelection{Mode: mode, Results: map[string]MaterializedResultSource{}}
		if selected {
			selection.Results["job"] = MaterializedResultSource{Ref: NodeRef{"source", "job"}, StateSeq: 1}
		}
		lineage, err := json.Marshal(executionLineage{Lineage: runspec.Lineage{RetryOf: "source"}, Retry: selection})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET lineage_json=? WHERE id='run'`, lineage)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET state_seq=1 WHERE run_id='source'`)
		tenant.s.materializations = materializationRecordsFunc(func(context.Context, *storeTx, Team, Actor, NodeRef, MaterializeSource) (materializedRecords, error) {
			return materializedRecords{}, nil
		})
		request.Source = &MaterializeSource{Kind: SourceManualRetry, Result: &MaterializedResultSource{Ref: NodeRef{run, job}, StateSeq: seq}}
		result, err := tenant.ReconcileNode(t.Context(), request)
		want := mode == "failed" && selected && run == "source" && job == "job" && seq == 1
		if (err == nil) != want || want && result.Kind != Materialize {
			t.Fatalf("uncommitted source accepted: mode=%q selected=%t source=%+v result=%+v error=%v", mode, selected, request.Source, result, err)
		}
		if !want {
			row, readErr := tenant.ReadNode(t.Context(), request.Ref)
			if readErr != nil || row.StateSeq != 0 || row.Status != StatusPending {
				t.Fatalf("refused selection changed work: %+v %v", row, readErr)
			}
		}
	})
}
