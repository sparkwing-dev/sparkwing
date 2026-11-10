package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/blob"

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

// safety: carrying a gate and its dependents in one transaction prevents a later
// cache miss from executing protected work behind an earlier run's approval.
func materializeRetryDependentsTx(ctx context.Context, tx *storeTx, team Team, gate nodeTransition) error {
	tx.retryGateGroup = true
	defer func() { tx.retryGateGroup = false }()
	snapshot, err := readSnapshotTx(ctx, tx, team, string(gate.Ref.RunID))
	if err != nil {
		return err
	}
	graph, err := snapshot.Scheduling()
	if err != nil {
		return err
	}
	deps := graph.Dependencies()
	group := map[string]bool{string(gate.Ref.NodeID): true}
	for changed := true; changed; {
		changed = false
		for id, needs := range deps {
			if !group[id] && slices.ContainsFunc(needs, func(dep string) bool { return group[dep] }) {
				group[id], changed = true, true
			}
		}
	}
	rows := make(map[string]runstate.NodeRow, len(snapshot.Nodes))
	for _, row := range snapshot.Nodes {
		// safety: an outstanding expansion can introduce a new dependent on the gate.
		if row.Expansion == runstate.ExpansionExpected {
			return blob.ErrNotFound
		}
		rows[string(row.Ref.NodeID)] = row
	}
	for id := range group {
		if _, selected := snapshot.Run.Retry.Results[id]; !selected {
			return blob.ErrNotFound
		}
		for _, dep := range deps[id] {
			if !group[dep] && (rows[dep].Status != runstate.StatusDone || rows[dep].Attempt != 0 || !scheduleSucceeded(string(rows[dep].Outcome))) {
				return blob.ErrNotFound
			}
		}
	}
	for _, row := range snapshot.Nodes {
		if !group[string(row.Ref.NodeID)] || row.Ref == gate.Ref {
			continue
		}
		current, err := readNodeTx(ctx, tx, team, row.Ref)
		if err != nil {
			return err
		}
		if current.Status == runstate.StatusDone {
			if current.Attempt != 0 || !scheduleSucceeded(string(current.Outcome)) {
				return blob.ErrNotFound
			}
			continue
		}
		selected := snapshot.Run.Retry.Results[string(row.Ref.NodeID)]
		source := runstate.MaterializeSource{Kind: runstate.SourceManualRetry, Result: &selected}
		_, err = transitionNodeStateTx(ctx, tx, team, nodeTransition{Ref: row.Ref, Actor: gate.Actor, Cause: causeMaterialize, Extra: transitionExtra{Materialize: &source}})
		if err != nil {
			if reuseRefused(err) {
				return blob.ErrNotFound
			}
			return err
		}
	}
	return nil
}
