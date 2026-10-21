package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

var (
	errLauncherHostPending   = errors.New("launcher execution host awaits activation")
	errCapacityAdmissionBusy = errors.New("capacity admission transaction is busy")
)

// CapacityBinding names durable work. Generation authenticates the current award;
// it is never part of a reservation's identity or arrival order.
type CapacityBinding struct {
	RunID      string
	NodeID     string
	Generation int64
}

// MayStart commits an award-bound permit, or a durable wait or terminal outcome.
// Job admission never reacquires the parent plan: plan capacity gates releases.
func (t *Tenant) MayStart(ctx context.Context, binding CapacityBinding, actor Actor) (AcquireSlotResponse, error) {
	var result AcquireSlotResponse
	err := t.s.inBlobTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = t.admitWorkTx(ctx, tx, binding, actor)
		if err != nil {
			return err
		}
		if result.Kind == AcquireSkipped || result.Kind == AcquireFailed {
			return settleAdmissionTx(ctx, tx, t.team, binding, actor, result)
		}
		return nil
	})
	return result, err
}

func (t *Tenant) admitWorkTx(ctx context.Context, tx *storeTx, binding CapacityBinding, actor Actor) (AcquireSlotResponse, error) {
	if binding.NodeID != "" && actor.Kind != ActorHolder {
		return AcquireSlotResponse{}, holderRefusal()
	}
	fence := capacityFence{binding: binding, actor: actor}
	if err := checkCapacityOwnerTx(ctx, tx, t.team, fence); err != nil {
		return AcquireSlotResponse{}, err
	}
	run, err := readRunTx(ctx, tx, t.team, binding.RunID)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	if run.Spec == nil {
		return AcquireSlotResponse{}, bindingRefusal()
	}
	groups, err := capacityGroupsTx(ctx, tx, t.team, binding.RunID, binding.NodeID)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	keys := make([]string, 0, len(groups))
	declarations := make(map[string]runspec.Concurrency, len(groups))
	for _, group := range groups {
		key, err := capacityKeyTx(ctx, tx, actor.Authority, binding.RunID, binding.NodeID, group.Scope, group.Group)
		if errors.Is(err, errLauncherHostPending) {
			return AcquireSlotResponse{Kind: AcquireQueued}, nil
		}
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		keys = append(keys, key)
		declarations[key] = group
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	lockKeys := slices.Clone(keys)
	rows, err := tx.QueryContext(ctx, `SELECT key FROM concurrency_holders WHERE team=? AND run_id=? AND node_id=? UNION SELECT key FROM concurrency_waiters WHERE team=? AND run_id=? AND node_id=?`, t.team, binding.RunID, binding.NodeID, t.team, binding.RunID, binding.NodeID)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return AcquireSlotResponse{}, errors.Join(err, rows.Close())
		}
		lockKeys = append(lockKeys, key)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return AcquireSlotResponse{}, err
	}
	slices.Sort(lockKeys)
	lockKeys = slices.Compact(lockKeys)
	if err := prepareCapacityAcquisitionTx(ctx, tx, t.team, lockKeys, fence); err != nil {
		if errors.Is(err, errCapacityAdmissionBusy) {
			return AcquireSlotResponse{Kind: AcquireQueued}, nil
		}
		return AcquireSlotResponse{}, err
	}
	// An approval has no execution award and never participates in job groups.
	if binding.NodeID != "" {
		row, err := readNodeTx(ctx, tx, t.team, controllerwire.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)})
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return AcquireSlotResponse{}, err
		}
		if job.Approval != nil {
			return AcquireSlotResponse{}, bindingRefusal()
		}
	}
	now := tx.now(ctx)
	expires, err := capacityLeaseCeilingTx(ctx, tx, t.team, binding.RunID, binding.NodeID, now.Add(defaultConcurrencyLease).UnixNano())
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	id := binding.RunID + "/" + nodeIDOrDash(binding.NodeID)
	var arrival, previousExpiry int64
	err = tx.QueryRowContext(ctx, `SELECT arrived_at,lease_expires_at FROM admission_work WHERE team=? AND run_id=? AND node_id=?`, t.team, binding.RunID, binding.NodeID).Scan(&arrival, &previousExpiry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AcquireSlotResponse{}, err
	}
	if err == nil && len(keys) > 0 {
		var count int
		var earliest sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(lease_expires_at) FROM (SELECT lease_expires_at FROM concurrency_holders WHERE team=? AND run_id=? AND node_id=? UNION ALL SELECT lease_expires_at FROM concurrency_waiters WHERE team=? AND run_id=? AND node_id=?) reservations`, t.team, binding.RunID, binding.NodeID, t.team, binding.RunID, binding.NodeID).Scan(&count, &earliest); err != nil {
			return AcquireSlotResponse{}, err
		}
		if count != len(keys) || !earliest.Valid || earliest.Int64 <= now.UnixNano() {
			previousExpiry = 0
		}
	}
	if err != nil || previousExpiry <= now.UnixNano() {
		if err := clearAdmissionLevelTx(ctx, tx, t.team, binding.RunID, binding.NodeID); err != nil {
			return AcquireSlotResponse{}, err
		}
		var latest sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(arrived_at) FROM admission_work WHERE team=?`, t.team).Scan(&latest); err != nil {
			return AcquireSlotResponse{}, err
		}
		arrival = max(now.UnixNano(), latest.Int64+1)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admission_work(team,run_id,node_id,arrived_at,lease_expires_at,generation,holder) VALUES(?,?,?,?,?,?,?) ON CONFLICT(team,run_id,node_id) DO UPDATE SET arrived_at=excluded.arrived_at,lease_expires_at=excluded.lease_expires_at,granted=0,generation=excluded.generation,holder=excluded.holder`, t.team, binding.RunID, binding.NodeID, arrival, expires, binding.Generation, actor.Holder); err != nil {
		return AcquireSlotResponse{}, err
	}
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		rows, err := tx.QueryContext(ctx, `SELECT key FROM `+table+` WHERE team=? AND run_id=? AND node_id=?`, t.team, binding.RunID, binding.NodeID)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		var obsolete []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return AcquireSlotResponse{}, errors.Join(err, rows.Close())
			}
			if !slices.Contains(keys, k) {
				obsolete = append(obsolete, k)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return AcquireSlotResponse{}, err
		}
		for _, k := range obsolete {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE team=? AND run_id=? AND node_id=? AND key=?`, t.team, binding.RunID, binding.NodeID, k); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
	}
	// Trial policy effects commit only if every group grants. A wait retains no
	// earlier group, and cancel_others cannot preempt before a later group waits.
	if _, err := tx.ExecContext(ctx, `SAVEPOINT admission_set`); err != nil {
		return AcquireSlotResponse{}, err
	}
	result := AcquireSlotResponse{Kind: AcquireGranted, HolderID: id, LeaseExpiresAt: time.Unix(0, expires)}
	for _, key := range keys {
		group := declarations[key]
		if group.QueueTimeoutMS != nil && *group.QueueTimeoutMS > 0 && !time.Unix(0, arrival).Add(time.Duration(*group.QueueTimeoutMS)*time.Millisecond).After(tx.now(ctx)) {
			result = AcquireSlotResponse{Kind: AcquireFailed, FailureReason: FailureQueueTimeout}
			break
		}
		result, err = t.acquireConcurrencySlotTx(ctx, tx, acquireSlotRequest{Key: key, HolderID: id, RunID: binding.RunID, NodeID: binding.NodeID}, &fence, time.Time{})
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		if result.Kind != AcquireGranted && result.Kind != AcquireCancellingOthers {
			break
		}
	}
	if result.Kind != AcquireGranted && result.Kind != AcquireCancellingOthers {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT admission_set`); err != nil {
			return AcquireSlotResponse{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT admission_set`); err != nil {
		return AcquireSlotResponse{}, err
	}
	if result.Kind == AcquireQueued {
		if err := clearAdmissionLevelTx(ctx, tx, t.team, binding.RunID, binding.NodeID); err != nil {
			return result, err
		}
		for _, key := range keys {
			group := declarations[key]
			if _, err := tx.ExecContext(ctx, `INSERT INTO concurrency_entries(team,key,capacity,last_write_run_id,last_write_node_id,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(team,key) DO NOTHING`, t.team, key, max(group.Capacity, 1), binding.RunID, binding.NodeID, now.UnixNano()); err != nil {
				return result, err
			}
			if err := txPark(ctx, tx, t.team, ConcurrencyWaiter{Key: key, RunID: binding.RunID, NodeID: binding.NodeID, HolderID: id, Policy: OnLimitQueue, Cost: max(group.Cost, 1), DeclaredCapacity: max(group.Capacity, 1)}, arrival, expires); err != nil {
				return result, err
			}
		}
	} else if result.Kind == AcquireGranted || result.Kind == AcquireCancellingOthers {
		if _, err := tx.ExecContext(ctx, `UPDATE admission_work SET granted=1 WHERE team=? AND run_id=? AND node_id=?`, t.team, binding.RunID, binding.NodeID); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET queue_arrived_at=? WHERE team=? AND run_id=? AND node_id=? AND holder_id=?`, arrival, t.team, binding.RunID, binding.NodeID, id); err != nil {
			return result, err
		}
		result.Kind, result.HolderID, result.LeaseExpiresAt = AcquireGranted, id, time.Unix(0, expires)
	}
	if err := checkCapacityOwnerTx(ctx, tx, t.team, fence); err != nil {
		return result, err
	}
	for _, key := range lockKeys {
		if err := txCheckConcurrencyInvariants(ctx, tx, t.team, key, tx.now(ctx).UnixNano()); err != nil {
			return result, err
		}
	}
	return result, nil
}

func settleAdmissionTx(ctx context.Context, tx *storeTx, team Team, b CapacityBinding, actor Actor, result AcquireSlotResponse) error {
	if b.NodeID == "" {
		kind := PolicyAdmissionFail
		if result.Kind == AcquireSkipped {
			kind = PolicyAdmissionSkip
		}
		if result.FailureReason == FailureQueueTimeout {
			kind = PolicyQueueTimeout
		}
		_, err := transitionRunTx(ctx, tx, team, RunTransition{RunID: runspec.RunID(b.RunID), Actor: actor, To: RunFailed, Cause: RunPolicyRefused, Reason: result.FailureReason, OwnerPolicy: &OwnerPolicyResult{Kind: kind, Generation: actor.Owner.Generation}})
		return err
	}
	row, err := readNodeTx(ctx, tx, team, controllerwire.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)})
	if err != nil {
		return err
	}
	outcome := NodeFailed
	if result.Kind == AcquireSkipped {
		outcome = NodeSkipped
	}
	attempt := row.Attempt
	_, err = transitionNodeTx(ctx, tx, team, nodeTransition{Ref: row.Ref, Actor: actor, To: outcome, Cause: CauseReport, SourceAttempt: &attempt, Extra: transitionExtra{Result: &NodeResult{Outcome: outcome, Reason: result.FailureReason}}})
	return err
}

func checkStartPermitTx(ctx context.Context, tx *storeTx, team Team, row NodeRow) error {
	if row.CancelRequestedAt != nil {
		return holderRefusal()
	}
	var granted bool
	err := tx.QueryRowContext(ctx, `SELECT granted=1 FROM admission_work WHERE team=? AND run_id=? AND node_id=? AND generation=? AND holder=? AND lease_expires_at>?`, team, row.Ref.RunID, row.Ref.NodeID, row.Generation, row.ClaimedBy, tx.now(ctx).UnixNano()).Scan(&granted)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !granted {
		return holderRefusal()
	}
	return err
}

// RelinquishStart returns a provisional burst award when compute cannot start it.
// Readiness keeps its original timestamp so placement ranking does not restart.
func (t *Tenant) RelinquishStart(ctx context.Context, b CapacityBinding, actor Actor) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := checkCapacityUnitTx(ctx, tx, t.team, capacityFence{binding: b, actor: actor}); err != nil {
			return err
		}
		ref := controllerwire.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)}
		row, err := readNodeTx(ctx, tx, t.team, ref)
		if err != nil {
			return err
		}
		if row.Status != StatusPending || row.ClaimedBy == "" {
			return holderRefusal()
		}
		ready, queued := row.ReadyAt, row.QueueStartedAt
		_, err = transitionNodeTx(ctx, tx, t.team, nodeTransition{Ref: ref, Actor: actor, To: nodePending, Cause: CauseRelease, SourceAttempt: &row.Attempt})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE nodes SET ready_at=?,queue_started_at=? WHERE team=? AND run_id=? AND node_id=?`, nullableTime(ready), nullableTime(queued), t.team, b.RunID, b.NodeID)
		return err
	})
}

func capacityKeyTx(ctx context.Context, tx *storeTx, authority capability.AuthorityRef, runID, nodeID, scope, group string) (string, error) {
	level := "job"
	if nodeID == "" {
		level = "plan"
		if scope == "box" {
			return "", bindingRefusal()
		}
	}
	switch scope {
	case "global":
		return runspec.ConcurrencyKey(level, scope, "", group), nil
	case "run":
		return runspec.ConcurrencyKey(level, scope, runID, group), nil
	case "box":
		chain, err := authorityChainTx(ctx, tx, authority, false)
		if err != nil {
			return "", err
		}
		c := chain[0].principal.Capability
		if c.Unit != "" && c.ExecutionHost != "" {
			return runspec.ConcurrencyKey(level, scope, c.ExecutionHost, group), nil
		}
		if c.Unit != "" {
			for _, node := range chain {
				if capability.Launcher(node.principal.Capability) {
					return "", errLauncherHostPending
				}
			}
		}
	}
	return "", bindingRefusal()
}

func admitReleaseTx(ctx context.Context, tx *storeTx, team Team, runID string, actor Actor) (bool, error) {
	t := &Tenant{s: tx.store, team: team}
	result, err := t.admitWorkTx(ctx, tx, CapacityBinding{RunID: runID, Generation: int64(actor.Owner.Generation)}, actor)
	if err != nil {
		return false, err
	}
	if result.Kind == AcquireSkipped || result.Kind == AcquireFailed {
		return false, settleAdmissionTx(ctx, tx, team, CapacityBinding{RunID: runID}, actor, result)
	}
	return result.Kind == AcquireGranted, nil
}

func clearAdmissionLevelTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) error {
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE team=? AND run_id=? AND node_id=?`, team, runID, nodeID); err != nil {
			return err
		}
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

// ReleaseEvaluation commits permission to evaluate an approval or source generators.
// Replays keep following an already released evaluation when plan capacity lapses.
func (t *Tenant) ReleaseEvaluation(ctx context.Context, ref controllerwire.NodeRef, actor Actor) (Applied, error) {
	var result Applied
	err := t.s.inBlobTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = releaseEvaluationTx(ctx, tx, t.team, ref, actor)
		return err
	})
	return result, err
}

func releaseEvaluationTx(ctx context.Context, tx *storeTx, team Team, ref controllerwire.NodeRef, actor Actor) (result Applied, err error) {
	if err := prepareStateActorTx(ctx, tx, team, string(ref.RunID), actor); err != nil {
		return result, err
	}
	if err := lockStateRunTx(ctx, tx, team, string(ref.RunID)); err != nil {
		return result, err
	}
	if err := checkOwnerTx(ctx, tx, team, actor, string(ref.RunID)); err != nil {
		return result, err
	}
	row, err := readNodeTx(ctx, tx, team, ref)
	if err != nil {
		return result, err
	}
	kind := "job_expansion_ready"
	if row.Status == StatusPending && row.ReadyAt == nil && row.ClaimedBy == "" {
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return result, err
		}
		if job.Approval == nil {
			return result, holderRefusal()
		}
		kind = "job_approval_ready"
	} else if row.Status != StatusDone || row.Outcome != NodeSuccess && row.Outcome != NodeMemoized || row.Expansion != ExpansionExpected {
		return result, holderRefusal()
	}

	released, err := evaluationReleasedTx(ctx, tx, team, ref, kind, row.Generation)
	if err != nil {
		return result, err
	}
	if released {
		result.Replayed = true
		return result, nil
	}
	admitted, err := admitReleaseTx(ctx, tx, team, string(ref.RunID), actor)
	if err != nil {
		return result, err
	}
	if !admitted {
		result.Waiting = true
		return result, nil
	}
	return result, appendStateEventTx(ctx, tx, team, ref, kind, 0, evaluationRelease{Generation: row.Generation}, tx.now(ctx))
}

type evaluationRelease struct {
	Generation runspec.ClaimGeneration `json:"generation"`
}

func evaluationReleasedTx(ctx context.Context, tx *storeTx, team Team, ref controllerwire.NodeRef, kind string, generation runspec.ClaimGeneration) (bool, error) {
	var payload []byte
	err := tx.QueryRowContext(ctx, `SELECT payload FROM events WHERE team=? AND run_id=? AND node_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, team, ref.RunID, ref.NodeID, kind).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var release evaluationRelease
	if err := json.Unmarshal(payload, &release); err != nil {
		return false, err
	}
	return release.Generation == generation, nil
}
