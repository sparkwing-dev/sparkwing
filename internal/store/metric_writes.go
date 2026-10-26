package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// WriteMetric records a resource sample under the current reporting authority.
func (t *Tenant) WriteMetric(ctx context.Context, a capability.Authorized, sample MetricSample) error {
	if err := sample.ValidateResourceValues(); err != nil {
		return err
	}
	if !time.Unix(0, sample.TS.UnixNano()).Equal(sample.TS) {
		return errors.New("node metric timestamp exceeds the nanosecond storage range")
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := t.metricAuthorityTx(ctx, tx, a, "metrics"); err != nil {
			return err
		}
		return insertMetricTx(ctx, tx, t.team, runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}, sample)
	})
}

// WriteUsage accumulates process accounting under the current reporting authority.
func (t *Tenant) WriteUsage(ctx context.Context, a capability.Authorized, usage NodeUsage) error {
	if usage.CPUTime < 0 || usage.Wall < 0 || usage.MaxRSSBytes < 0 {
		return errors.New("node usage requires nonnegative CPU time, wall time and peak RSS")
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := t.metricAuthorityTx(ctx, tx, a, "usage"); err != nil {
			return err
		}
		return addUsageTx(ctx, tx, t.team, runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}, usage)
	})
}

func (t *Tenant) metricAuthorityTx(ctx context.Context, tx *storeTx, a capability.Authorized, kind string) error {
	if a.Target.Team != string(t.team) || a.Target.RunID == "" || a.Target.NodeID == "" || a.ResultReplay || a.LogTailAttempt != 0 {
		return bindingRefusal()
	}
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return err
	}
	if err := lockOwnerUnitTx(ctx, tx, t.team, UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(a.Target.RunID)}); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := lockStateRunTx(ctx, tx, t.team, a.Target.RunID); err != nil {
		return err
	}
	c, b := p.Capability, p.Capability.Binding
	if c.Team != string(t.team) {
		return bindingRefusal()
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/"+kind)
	if !ok {
		return bindingRefusal()
	}
	for i, rule := range route.Access {
		if rule.Credential != a.Access.Credential || rule.Binding != a.Access.Binding || rule.Liveness != a.Access.Liveness || !slices.Equal(rule.Grants, a.Access.Grants) {
			continue
		}
		if rule.Credential == capability.CredentialClaim {
			actor := runstate.Actor{Authority: p.Authority, CapabilityPrefix: c.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID), Holder: b.Holder, Generation: runspec.ClaimGeneration(b.Generation)}}
			switch rule.Binding {
			case capability.BindOwnNode:
				actor.Kind = runstate.ActorHolder
				row, err := readNodeTx(ctx, tx, t.team, runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)})
				if err != nil {
					return err
				}
				if err := checkHolderTx(ctx, tx, t.team, row, actor); err != nil {
					return err
				}
				if row.FinishedAt != nil || row.Attempt != runspec.AttemptOrdinal(a.Binding.Attempt) || runspec.AttemptOrdinal(b.Attempt) > 0 && runspec.AttemptOrdinal(b.Attempt) != row.Attempt {
					return holderRefusal()
				}
				b.Attempt = int64(row.Attempt)
			case capability.BindRunOfTrigger:
				actor.Kind = runstate.ActorRunHost
				actor.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(b.RunID), Holder: b.Holder, Generation: runspec.ClaimGeneration(b.Generation)}}
				if err := checkOwnerTx(ctx, tx, t.team, actor, a.Target.RunID); err != nil {
					return err
				}
			default:
				return bindingRefusal()
			}
			run, err := readRunTx(ctx, tx, t.team, a.Target.RunID)
			if err != nil {
				return err
			}
			if run.FinishedAt != nil || b.Kind != a.Binding.Kind || b.RunID != a.Binding.RunID || b.NodeID != a.Binding.NodeID || b.Holder != a.Binding.Holder || b.Generation != a.Binding.Generation || b.Attempt != a.Binding.Attempt {
				return holderRefusal()
			}
		}
		_, err := capability.Authorize(ctx, tx, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: i}}}, a.Target)
		return err
	}
	return bindingRefusal()
}

// NodeUsage records process CPU time, peak resident memory and elapsed wall time.
type NodeUsage struct {
	CPUTime     time.Duration
	MaxRSSBytes int64
	Wall        time.Duration
}

func addUsageTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef, u NodeUsage) error {
	runID, nodeID := ref.RunID, ref.NodeID
	var cpu, wall int64
	err := tx.QueryRowContext(ctx, `SELECT cpu_nanos, process_wall_nanos FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(), team, runID, nodeID).Scan(&cpu, &wall)
	if err != nil {
		return err
	}
	if cpu < 0 || wall < 0 {
		return errors.New("stored node usage has negative CPU or wall time")
	}
	if cpu > math.MaxInt64-int64(u.CPUTime) {
		return errors.New("node CPU time overflow")
	}
	if wall > math.MaxInt64-int64(u.Wall) {
		return errors.New("node wall time overflow")
	}
	_, err = tx.execContext(ctx, `
UPDATE nodes
   SET cpu_nanos = cpu_nanos + ?,
       process_wall_nanos = process_wall_nanos + ?,
       max_rss_bytes = CASE WHEN ? > max_rss_bytes THEN ? ELSE max_rss_bytes END
 WHERE team = ? AND run_id = ? AND node_id = ?`,
		int64(u.CPUTime), int64(u.Wall), u.MaxRSSBytes, u.MaxRSSBytes, team, runID, nodeID)
	if err != nil {
		return err
	}
	return nil
}
