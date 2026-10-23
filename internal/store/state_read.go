package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func readRunTx(ctx context.Context, tx *storeTx, team Team, id string) (RunRow, error) {
	v := RunRow{Team: team, Execution: runstate.Execution{ID: id}}
	var request, spec, lineage, pools, actor, plan []byte
	var cancel, start, finish sql.NullInt64
	var created int64
	var controller bool
	var execution executionLineage
	err := tx.QueryRowContext(ctx, `SELECT number,pipeline,status,request_json,spec_json,lineage_json,plan_json,plan_hash,dispatch,allowed_pools,cancel_requested_at,cancel_reason,started_at,finished_at,state_seq,state_digest,state_actor,created_at,`+controllerRunOwnership+` FROM runs r WHERE team=? AND id=?`, team, id).Scan(&v.Number, &v.Pipeline, &v.State, &request, &spec, &lineage, &plan, &v.PlanHash, &v.Dispatch, &pools, &cancel, &v.CancelReason, &start, &finish, &v.StateSeq, &v.StateDigest, &actor, &created, &controller)
	if errors.Is(err, sql.ErrNoRows) {
		return v, notFound("run", id)
	}
	if err != nil {
		return v, err
	}
	for _, item := range []struct {
		raw []byte
		dst any
	}{{request, &v.Request}, {spec, &v.Spec}, {lineage, &execution}, {pools, &v.AllowedPools}, {actor, &v.StateActor}} {
		if len(item.raw) > 0 {
			if err := json.Unmarshal(item.raw, item.dst); err != nil {
				return v, authorityError(apierr.IntegrityFailure, "invalid persisted run: "+err.Error())
			}
		}
	}
	v.expansions, err = readAcceptedExpansionsTx(ctx, tx, team, id)
	if err != nil {
		return v, err
	}
	v.Lineage, v.Retry = execution.Lineage, execution.Retry
	v.admission = execution.admissionIdentity
	v.Plan = plan
	v.CreatedAt = time.Unix(0, created).UTC()
	v.CancelRequestedAt, v.StartedAt, v.FinishedAt = stateTime(cancel), stateTime(start), stateTime(finish)
	if v.Request.Request.Mode == runspec.IntakePublish {
		return v, nil
	}
	if controller {
		var owner runstate.OwnerLeaseRef
		owner.Kind, owner.Name, owner.RunID = runstate.OwnerController, "controller.owner", runspec.RunID(id)
		err = tx.QueryRowContext(ctx, `SELECT holder,generation FROM leases WHERE name='controller.owner'`).Scan(&owner.Holder, &owner.Generation)
		if err == nil {
			v.Owner = &owner
		} else if !errors.Is(err, sql.ErrNoRows) {
			return v, err
		}
	} else {
		owner := runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(id)}}
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

const nodeReadColumns = `status,outcome,status_detail,COALESCE(claimed_by,''),capability_prefix,owner_capability_prefix,generation,attempt,disruptions,ready_at,queue_started_at,lease_expires_at,started_at,finished_at,cancel_requested_at,cancel_reason,state_seq,state_digest,state_actor,expansion,expansion_digest,execution_json,spec_json,spec_hash,output_record,manifest_record`

func readNodeTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef) (runstate.NodeRow, error) {
	v := runstate.NodeRow{Ref: ref}
	var ready, queued, lease, start, finish, cancel sql.NullInt64
	var actor, execution, output, manifest []byte
	err := tx.QueryRowContext(ctx, `SELECT `+nodeReadColumns+` FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, ref.RunID, ref.NodeID).Scan(&v.Status, &v.Outcome, &v.StatusDetail, &v.ClaimedBy, &v.CapabilityPrefix, &v.OwnerCapabilityPrefix, &v.Generation, &v.Attempt, &v.Disruptions, &ready, &queued, &lease, &start, &finish, &cancel, &v.CancelReason, &v.StateSeq, &v.StateDigest, &actor, &v.Expansion, &v.ExpansionDigest, &execution, &v.Spec, &v.SpecHash, &output, &manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return v, notFound("job", string(ref.NodeID))
	}
	if err != nil {
		return v, err
	}
	for _, part := range []struct {
		raw []byte
		key **blob.RecordKey
	}{{output, &v.OutputRecord}, {manifest, &v.ManifestRecord}} {
		if len(part.raw) > 0 {
			if err := json.Unmarshal(part.raw, part.key); err != nil {
				return v, authorityError(apierr.IntegrityFailure, "invalid persisted job record: "+err.Error())
			}
		}
	}
	if len(actor) > 0 {
		if err = json.Unmarshal(actor, &v.StateActor); err != nil {
			return v, authorityError(apierr.IntegrityFailure, "invalid persisted job actor: "+err.Error())
		}
	}
	if err = json.Unmarshal(execution, &v.Execution); err != nil {
		return v, authorityError(apierr.IntegrityFailure, "invalid persisted execution: "+err.Error())
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

func readAcceptedExpansionsTx(ctx context.Context, tx *storeTx, team Team, id string) ([]runstate.AcceptedExpansionRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,generation,expansion_body,expansion_digest,expansion_registration FROM nodes WHERE team=? AND run_id=? AND expansion_body IS NOT NULL ORDER BY seq,node_id`, team, id)
	if err != nil {
		return nil, err
	}
	var out []runstate.AcceptedExpansionRow
	for rows.Next() {
		var node string
		var generation int64
		var body []byte
		var digest string
		var registrationRaw []byte
		if err = rows.Scan(&node, &generation, &body, &digest, &registrationRaw); err != nil {
			_ = rows.Close()
			return out, err
		}
		ref := runspec.NodeRef{RunID: runspec.RunID(id), NodeID: runspec.NodeID(node)}
		if len(body) > 0 {
			if len(registrationRaw) == 0 {
				_ = rows.Close()
				return out, errors.New("accepted expansion is missing host masking registrations; start a new run instead of resuming")
			}
			bodyHash, err := runspec.CanonicalHash(json.RawMessage(body))
			if err != nil {
				_ = rows.Close()
				return out, err
			}
			var registration runspec.SecretRegistration
			if err := json.Unmarshal(registrationRaw, &registration); err != nil {
				_ = rows.Close()
				return out, err
			}
			if registration.Version != 1 {
				_ = rows.Close()
				return out, errors.New("accepted expansion was sealed without host masking registrations; start a new run instead of resuming")
			}
			projected, err := runspec.BuildSecretRegistration(runspec.Request{}, runspec.Resolved{SecretRegistration: registration})
			if err != nil || !reflect.DeepEqual(projected, registration) {
				_ = rows.Close()
				return out, errors.New("persisted expansion registration differs from its projection")
			}
			committed, err := runspec.CanonicalHash(struct {
				Body         json.RawMessage
				Registration runspec.SecretRegistration
			}{body, registration})
			if err != nil || string(committed) != digest {
				_ = rows.Close()
				return out, errors.New("persisted expansion registration differs from its accepted digest")
			}
			out = append(out, runstate.AcceptedExpansionRow{Source: ref, SourceGeneration: generation, Body: body, BodyHash: string(bodyHash), Registration: registration})
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	return out, nil
}

func readSnapshotTx(ctx context.Context, tx *storeTx, team Team, id string) (RunSnapshot, error) {
	out := RunSnapshot{StoreNow: tx.now(ctx)}
	var err error
	if out.Run, err = readRunTx(ctx, tx, team, id); err != nil {
		return out, err
	}
	out.Expansions = out.Run.expansions
	rows, err := tx.QueryContext(ctx, `SELECT node_id FROM nodes WHERE team=? AND run_id=? ORDER BY seq,node_id`, team, id)
	if err != nil {
		return out, err
	}
	var refs []runspec.NodeRef
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			_ = rows.Close()
			return out, err
		}
		refs = append(refs, runspec.NodeRef{RunID: runspec.RunID(id), NodeID: runspec.NodeID(node)})
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
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
