package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ReadFailure returns the persisted masked failure and stage for a declared recovery source.
// The transaction checks the reader's recovery edge and exact source generation.
// A terminal status without recorded failure is not a synthetic SDK failure.
func (t *Tenant) ReadFailure(ctx context.Context, source NodeRef, generation int64, actor Actor) (*nodeproto.JobFailure, error) {
	if source.RunID == "" || source.NodeID == "" || generation < 0 {
		return nil, authorityError(apierr.BadRequest, "failure read requires a source generation")
	}
	var failure *nodeproto.JobFailure
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		principal, err := resolveActorTx(ctx, tx, actor)
		if err != nil {
			return err
		}
		c := principal.Capability
		if c.Team != string(t.team) || c.Audience != capability.Controller || !slices.Contains(c.Grants, capability.DataRead) {
			return holderRefusal()
		}
		if err := prepareStateActorTx(ctx, tx, t.team, source.RunID, actor, tx.now()); err != nil {
			return err
		}
		if err := lockStateRunTx(ctx, tx, t.team, source.RunID); err != nil {
			return err
		}
		snapshot, err := readSnapshotTx(ctx, tx, t.team, source.RunID)
		if err != nil {
			return err
		}
		if snapshot.Run.CancelRequestedAt != nil || snapshot.Run.FinishedAt != nil {
			return holderRefusal()
		}
		if actor.Kind == ActorRunHost {
			if err := checkOwnerTx(ctx, tx, t.team, actor, source.RunID, tx.now()); err != nil {
				return err
			}
		} else if actor.Kind != ActorHolder || c.Binding.Kind != capability.BindingNode || c.Binding.RunID != source.RunID {
			return holderRefusal()
		}
		declared := false
		var parent *NodeRow
		for i := range snapshot.Nodes {
			row := &snapshot.Nodes[i]
			if row.Ref == source {
				parent = row
			}
			if actor.Kind == ActorHolder && row.Ref.NodeID != c.Binding.NodeID {
				continue
			}
			var job nodeproto.JobSpec
			if err := json.Unmarshal(row.Spec, &job); err != nil {
				return err
			}
			if job.OnFailureOf != source.NodeID {
				continue
			}
			if actor.Kind == ActorHolder {
				if err := checkHolderTx(ctx, tx, t.team, *row, actor, tx.now()); err != nil {
					return err
				}
			}
			declared = true
		}
		if !declared || parent == nil || parent.Status != StatusDone || parent.Generation != generation {
			return holderRefusal()
		}
		if parent.Outcome != NodeFailed {
			return nil
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT failure_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, t.team, source.RunID, source.NodeID).Scan(&raw); err != nil {
			return err
		}
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, &failure); err != nil {
			return err
		}
		if failure == nil {
			return authorityError(apierr.IntegrityFailure, "recorded failure is null")
		}
		return validateRecordedFailure(source, failure)
	})
	if err != nil {
		return nil, err
	}
	return failure, nil
}
