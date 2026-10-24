package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func selectRetryTx(ctx context.Context, tx *storeTx, team Team, source string, selection *runstate.RetrySelection) error {
	selection.Results = map[string]runstate.MaterializedResultSource{}
	if selection.Mode == "all" {
		return nil
	}
	rows, err := tx.queryContext(ctx, `SELECT node_id,state_seq FROM nodes WHERE team=? AND run_id=? AND status='done' AND outcome IN ('success','memoized')`, team, source)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		result := runstate.MaterializedResultSource{Ref: runspec.NodeRef{RunID: runspec.RunID(source)}}
		if err := rows.Scan(&result.Ref.NodeID, &result.StateSeq); err != nil {
			return err
		}
		selection.Results[string(result.Ref.NodeID)] = result
	}
	return rows.Err()
}

type executionLineage struct {
	runspec.Lineage
	admissionIdentity
	Retry *runstate.RetrySelection `json:"retry,omitempty"`
}

// admissionIdentity stays outside source seals and public display lineage.
type admissionIdentity struct {
	AdmissionSubject capability.Subject `json:"admission_subject,omitzero"`
	AdmissionKey     string             `json:"admission_key,omitempty"`
}
