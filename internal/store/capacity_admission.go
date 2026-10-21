package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

var (
	errLauncherHostPending   = errors.New("launcher execution host awaits activation")
	errCapacityAdmissionBusy = errors.New("capacity admission transaction is busy")
)

// AdmitCapacity admits the sealed plan capacity before the unit's job capacity.
// Repeated calls adopt retained holders and waiters under the current execution award.
func (t *Tenant) AdmitCapacity(ctx context.Context, binding CapacityBinding, actor Actor) (AcquireSlotResponse, error) {
	var result AcquireSlotResponse
	err := t.s.inTx(ctx, func(tx *storeTx) (err error) {
		result, err = t.admitCapacityTx(ctx, tx, binding, actor)
		return err
	})
	return result, err
}

type capacityStage struct {
	fence capacityFence
	keys  []string
}

func (t *Tenant) admitCapacityTx(ctx context.Context, tx *storeTx, binding CapacityBinding, actor Actor) (result AcquireSlotResponse, err error) {
	original := capacityFence{binding: binding, actor: actor}
	run, err := readRunTx(ctx, tx, t.team, binding.RunID)
	if err != nil {
		return result, err
	}
	if run.Spec == nil {
		return result, bindingRefusal()
	}
	plan := original
	evaluation, err := capacityEvaluationTx(ctx, tx, t.team, binding, actor)
	if err != nil {
		return result, err
	}
	if binding.NodeID != "" {
		generation, err := capacityGenerationTx(ctx, tx, t.team, binding.RunID, "")
		if err != nil {
			return result, err
		}
		plan.binding = CapacityBinding{RunID: binding.RunID, Generation: generation}
		plan.source = &binding
	}
	stages := []capacityStage{{fence: plan}}
	if binding.NodeID != "" && !evaluation {
		stages = append(stages, capacityStage{fence: original})
	}
	var keys []string
	pendingHost := false
	for i := range stages {
		stage := &stages[i]
		groups, err := capacityGroupsTx(ctx, tx, t.team, binding.RunID, stage.fence.binding.NodeID)
		if err != nil {
			return result, err
		}
		for _, group := range groups {
			// safety: a controller releases work before an execution award supplies its host.
			if group.Scope == "box" && actor.Kind == ActorController && binding.NodeID == "" {
				continue
			}
			key, err := capacityKeyTx(ctx, tx, actor.Authority, binding.RunID, group.Scope, group.Group)
			if errors.Is(err, errLauncherHostPending) {
				pendingHost = true
				continue
			}
			if err != nil {
				return result, err
			}
			stage.keys = append(stage.keys, key)
			keys = append(keys, key)
		}
		slices.SortFunc(stage.keys, strings.Compare)
	}
	// safety: database locks use one global order even though admission visits plan before job.
	slices.SortFunc(keys, strings.Compare)
	keys = slices.Compact(keys)
	if err := prepareCapacityAcquisitionTx(ctx, tx, t.team, keys, original); err != nil {
		if errors.Is(err, errCapacityAdmissionBusy) {
			return AcquireSlotResponse{Kind: AcquireQueued}, nil
		}
		return result, err
	}
	for _, stage := range stages {
		if err := checkCapacityUnitTx(ctx, tx, t.team, stage.fence); err != nil {
			return result, err
		}
		for _, key := range stage.keys {
			if err := checkCapacityFenceTx(ctx, tx, t.team, key, stage.fence); err != nil {
				return result, err
			}
		}
	}
	defer func() {
		if err != nil {
			return
		}
		if err = checkCapacityOwnerTx(ctx, tx, t.team, original); err != nil {
			return
		}
		for _, key := range keys {
			if err = txCheckConcurrencyInvariants(ctx, tx, t.team, key, tx.now().UnixNano()); err != nil {
				return
			}
		}
	}()
	if pendingHost {
		return AcquireSlotResponse{Kind: AcquireQueued}, nil
	}
	for i, stage := range stages {
		result, err = t.admitCapacityStageTx(ctx, tx, stage)
		if err != nil {
			return result, err
		}
		if result.Kind != AcquireGranted {
			// safety: recovery may retain a job holder after its earlier plan holder expires.
			// Drop those later resources before waiting for the plan again.
			if i == 0 && binding.NodeID != "" && !evaluation {
				if err := releaseCapacityTx(ctx, tx, t.team, binding.RunID, binding.NodeID); err != nil {
					return result, err
				}
			}
			return result, nil
		}
	}
	return result, nil
}

func (t *Tenant) admitCapacityStageTx(ctx context.Context, tx *storeTx, stage capacityStage) (AcquireSlotResponse, error) {
	binding, fence := stage.fence.binding, stage.fence
	id := binding.RunID + "/" + binding.NodeID
	if binding.NodeID == "" {
		id += "-"
	}
	result := AcquireSlotResponse{Kind: AcquireGranted, HolderID: id}
	for _, key := range stage.keys {
		if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
			return result, err
		}
		adopted, err := adoptCapacityTx(ctx, tx, t.team, key, id, binding)
		if err != nil {
			return result, err
		}
		switch adopted {
		case adoptHolder:
			continue
		case adoptWaiter:
			resolved, err := t.resolveWaiterTx(ctx, tx, key, binding.RunID, binding.NodeID, "", "", defaultConcurrencyLease, &fence)
			if err != nil {
				return result, err
			}
			switch resolved.Status {
			case waiterPromoted:
				continue
			case waiterStillWaiting:
				result = AcquireSlotResponse{Kind: AcquireQueued, Position: resolved.Position, QueueLength: resolved.QueueLength, Holders: resolved.Holders}
			case waiterExpired:
				result = AcquireSlotResponse{Kind: AcquireFailed, FailureReason: FailureQueueTimeout}
			case waiterCancelled:
				return result, ErrConcurrencySuperseded
			default:
				return result, bindingRefusal()
			}
		case acquireAgain:
			result, err = t.acquireConcurrencySlotTx(ctx, tx, acquireSlotRequest{Key: key, HolderID: id, RunID: binding.RunID, NodeID: binding.NodeID}, &fence, time.Time{})
			if err != nil {
				return result, err
			}
			if result.Kind == AcquireGranted || result.Kind == AcquireCancellingOthers {
				continue
			}
		default:
			return result, bindingRefusal()
		}
		return result, nil
	}
	var expires sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(lease_expires_at) FROM concurrency_holders WHERE team=? AND run_id=? AND node_id=? AND holder_id=? AND superseded=0`, t.team, binding.RunID, binding.NodeID, id).Scan(&expires); err != nil {
		return result, err
	}
	result.Kind, result.HolderID = AcquireGranted, id
	if expires.Valid {
		result.LeaseExpiresAt = time.Unix(0, expires.Int64)
	}
	return result, nil
}

// capacityKeyTx reads only the execution identity sealed on the authenticated award.
func capacityKeyTx(ctx context.Context, tx *storeTx, authority capability.AuthorityRef, runID, scope, group string) (string, error) {
	switch scope {
	case "global":
		return runspec.ConcurrencyKey(scope, "", group), nil
	case "run":
		return runspec.ConcurrencyKey(scope, runID, group), nil
	case "box":
		chain, err := authorityChainTx(ctx, tx, authority, false)
		if err != nil {
			return "", err
		}
		c := chain[0].principal.Capability
		if c.Unit != "" && c.ExecutionHost != "" {
			return runspec.ConcurrencyKey(scope, c.ExecutionHost, group), nil
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
