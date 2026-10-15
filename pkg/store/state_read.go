package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func readRunTx(ctx context.Context, tx *storeTx, team Team, id string) (RunRow, error) {
	v := RunRow{ID: id, Team: team}
	var request, spec, lineage, pools, actor, plan []byte
	var cancel, start, finish sql.NullInt64
	var created, accepted int64
	err := tx.QueryRowContext(ctx, `SELECT number,pipeline,status,request_json,spec_json,lineage_json,plan_json,plan_hash,dispatch,allowed_pools,cancel_requested_at,started_at,finished_at,state_seq,state_digest,state_actor,created_at,plan_accepted_generation FROM runs WHERE team=? AND id=?`, team, id).Scan(&v.Number, &v.Pipeline, &v.State, &request, &spec, &lineage, &plan, &v.PlanHash, &v.Dispatch, &pools, &cancel, &start, &finish, &v.StateSeq, &v.StateDigest, &actor, &created, &accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return v, notFound("run", id)
	}
	if err != nil {
		return v, err
	}
	for _, item := range []struct {
		raw []byte
		dst any
	}{{request, &v.Request}, {spec, &v.Spec}, {lineage, &v.Lineage}, {pools, &v.AllowedPools}, {actor, &v.StateActor}} {
		if len(item.raw) > 0 {
			if err := json.Unmarshal(item.raw, item.dst); err != nil {
				return v, fmt.Errorf("decode run %s: %w", id, err)
			}
		}
	}
	v.Plan = plan
	v.CreatedAt = time.Unix(0, created).UTC()
	v.CancelRequestedAt, v.StartedAt, v.FinishedAt = stateTime(cancel), stateTime(start), stateTime(finish)
	if v.Request.Request.Mode == runspec.IntakePublish {
		return v, nil
	}
	if v.Dispatch == "controller" && accepted > 0 {
		var owner OwnerLeaseRef
		owner.Kind, owner.Name, owner.RunID = OwnerController, "controller.owner", id
		err = tx.QueryRowContext(ctx, `SELECT holder,generation FROM leases WHERE name='controller.owner'`).Scan(&owner.Holder, &owner.Generation)
		if err == nil {
			v.Owner = &owner
		} else if !errors.Is(err, sql.ErrNoRows) {
			return v, err
		}
	} else {
		owner := OwnerLeaseRef{Kind: OwnerTrigger, RunID: id}
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(claimed_by,''),generation FROM triggers WHERE team=? AND id=?`, team, id).Scan(&owner.Holder, &owner.Generation)
		if err == nil {
			v.Owner = &owner
		} else if !errors.Is(err, sql.ErrNoRows) {
			return v, err
		}
	}
	return v, nil
}

func runClaimWait(run RunRow) runspec.Setting[time.Duration] {
	if run.Spec != nil {
		return run.Spec.Resolved.Settings.Options.ClaimWait
	}
	return run.Request.Request.Options.ClaimWait
}

const nodeReadColumns = `status,outcome,status_detail,COALESCE(claimed_by,''),capability_prefix,owner_capability_prefix,generation,attempt,disruptions,ready_at,queue_started_at,lease_expires_at,started_at,finished_at,cancel_requested_at,state_seq,state_digest,state_actor,expansion,expansion_digest,execution_json,spec_json,spec_hash`

func readNodeTx(ctx context.Context, tx *storeTx, team Team, ref NodeRef) (NodeRow, error) {
	v := NodeRow{Ref: ref}
	var ready, queued, lease, start, finish, cancel sql.NullInt64
	var actor, execution []byte
	err := tx.QueryRowContext(ctx, `SELECT `+nodeReadColumns+` FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, ref.RunID, ref.NodeID).Scan(&v.Status, &v.Outcome, &v.StatusDetail, &v.ClaimedBy, &v.CapabilityPrefix, &v.OwnerCapabilityPrefix, &v.Generation, &v.Attempt, &v.Disruptions, &ready, &queued, &lease, &start, &finish, &cancel, &v.StateSeq, &v.StateDigest, &actor, &v.Expansion, &v.ExpansionDigest, &execution, &v.Spec, &v.SpecHash)
	if errors.Is(err, sql.ErrNoRows) {
		return v, notFound("job", ref.NodeID)
	}
	if err != nil {
		return v, err
	}
	if len(actor) > 0 {
		if err = json.Unmarshal(actor, &v.StateActor); err != nil {
			return v, err
		}
	}
	if err = json.Unmarshal(execution, &v.Execution); err != nil {
		return v, err
	}
	// safety: application retry is node-wide; counting attempts within each generation excludes lease-loss attempts.
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(n-1),0) FROM (SELECT COUNT(*) AS n FROM node_attempts WHERE team=? AND run_id=? AND node_id=? GROUP BY generation) AS attempts`, team, ref.RunID, ref.NodeID).Scan(&v.ApplicationRetries)
	if err != nil {
		return v, err
	}
	v.ReadyAt, v.QueueStartedAt, v.LeaseExpiresAt, v.StartedAt, v.FinishedAt, v.CancelRequestedAt = stateTime(ready), stateTime(queued), stateTime(lease), stateTime(start), stateTime(finish), stateTime(cancel)
	if _, err = nodeState(v); err != nil {
		return v, err
	}
	return v, nil
}

func stateTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(0, v.Int64).UTC()
	return &t
}

func readSnapshotTx(ctx context.Context, tx *storeTx, team Team, id string) (RunSnapshot, error) {
	out := RunSnapshot{StoreNow: tx.now()}
	var err error
	if out.Run, err = readRunTx(ctx, tx, team, id); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id,generation,expansion_body,expansion_digest FROM nodes WHERE team=? AND run_id=? ORDER BY seq,node_id`, team, id)
	if err != nil {
		return out, err
	}
	var refs []NodeRef
	for rows.Next() {
		var node string
		var generation int64
		var body []byte
		var digest string
		if err = rows.Scan(&node, &generation, &body, &digest); err != nil {
			_ = rows.Close()
			return out, err
		}
		ref := NodeRef{id, node}
		refs = append(refs, ref)
		if len(body) > 0 {
			out.Expansions = append(out.Expansions, AcceptedExpansionRow{Source: ref, SourceGeneration: generation, Body: body, BodyHash: digest})
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, ref := range refs {
		v, err := readNodeTx(ctx, tx, team, ref)
		if err != nil {
			return out, err
		}
		out.Nodes = append(out.Nodes, v)
	}
	return out, nil
}
