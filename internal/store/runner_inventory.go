package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ReadRunners reads this tenant's accepted runner observations and current awards in one snapshot.
func (t *Tenant) ReadRunners(ctx context.Context) (capability.RunnerSnapshot, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.RunnerSnapshot{}, err
	}
	defer rollbackOrLog(tx)
	out := capability.RunnerSnapshot{Now: tx.time, Runners: []capability.RunnerObservation{}}
	indices := map[string]int{}
	agents, err := registeredAgentsTx(ctx, tx, t.team)
	if err != nil {
		return capability.RunnerSnapshot{}, err
	}
	for _, agent := range agents {
		row := capability.RunnerObservation{Team: string(t.team), Runner: nodeproto.RunnerIdentity{Name: agent.Name, Type: "runner", Labels: slices.Clone(agent.Profile.Labels)}, Location: agent.Profile.Location, LastSeen: agent.LastSeen, ActiveJobs: []string{}}
		if row.LastSeen.After(out.Now) {
			return capability.RunnerSnapshot{}, authorityError(apierr.IntegrityFailure, "runner observation is in the future")
		}
		if poll := agent.poll; poll != nil {
			row.Headroom = &capability.RunnerHeadroom{Cores: poll.Available.Cores, MemoryBytes: poll.Available.MemoryBytes, ObservedAt: poll.ObservedAt}
		}
		indices[agent.CapabilityPrefix] = len(out.Runners)
		out.Runners = append(out.Runners, row)
	}
	prefixes, err := currentRunnerAwardsTx(ctx, tx, t.team)
	if err != nil {
		return capability.RunnerSnapshot{}, err
	}
	for _, prefix := range prefixes {
		if err := appendRunnerAwardTx(ctx, tx, t.team, prefix, indices, &out); err != nil {
			return capability.RunnerSnapshot{}, err
		}
	}
	for i := range out.Runners {
		slices.Sort(out.Runners[i].ActiveJobs)
		out.Runners[i].ActiveJobs = slices.Compact(out.Runners[i].ActiveJobs)
	}
	if err := tx.Commit(); err != nil {
		return capability.RunnerSnapshot{}, err
	}
	return out, nil
}

func unavailableRunner(err error) bool {
	var denied *apierr.Error
	return errors.As(err, &denied) && denied.Code == apierr.Unauthenticated
}

func currentRunnerAwardsTx(ctx context.Context, tx *storeTx, team Team) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.prefix FROM capabilities c JOIN runs r ON r.team=c.team AND r.id=c.bind_run
	WHERE c.team=? AND c.audience='controller' AND c.issuance IN ('award','exchange') AND r.finished_at IS NULL AND r.status IN ('pending','running')
 AND `+liveCredentialSQL("c.")+` AND (
 EXISTS (SELECT 1 FROM nodes n WHERE c.unit_kind='node' AND n.team=c.team AND n.run_id=c.bind_run AND n.node_id=c.bind_node
 AND n.capability_prefix=c.prefix AND n.generation=c.bind_generation AND n.claimed_by=c.bind_holder AND n.status<>'done' AND n.lease_expires_at>?)
 OR EXISTS (SELECT 1 FROM triggers t WHERE c.unit_kind=t.unit_kind AND t.team=c.team AND t.id=c.bind_run
 AND t.capability_prefix=c.prefix AND t.generation=c.bind_generation AND t.claimed_by=c.bind_holder AND t.status='claimed' AND t.finished_at IS NULL AND t.lease_expires_at>?)
	OR EXISTS (SELECT 1 FROM eval_units e JOIN leases l ON l.name=e.owner_lease_name
	JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id AND n.generation=e.source_generation
	WHERE c.unit_kind='eval' AND e.team=c.team AND e.run_id=c.bind_run AND n.status='done' AND n.outcome IN ('success','memoized') AND n.expansion='expected'
 AND e.source_node_id=c.bind_node AND e.source_generation=c.bind_generation AND e.capability_prefix=c.prefix AND e.generation=c.eval_unit_generation
 AND e.claimed_by=c.bind_holder AND e.status IN ('claimed','running') AND e.lease_expires_at>? AND e.owner_lease_name='controller.owner' AND e.owner_generation=l.generation AND l.until>?))
 ORDER BY c.prefix`, team, tx.time.UnixNano(), tx.time.UnixNano(), tx.time.UnixNano(), tx.time.UnixNano(), tx.time.UnixNano(), tx.time.UnixNano())
	if err != nil {
		return nil, err
	}
	return scanPrefixes(rows)
}

func appendRunnerAwardTx(ctx context.Context, tx *storeTx, team Team, prefix string, indices map[string]int, out *capability.RunnerSnapshot) error {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false)
	if unavailableRunner(err) {
		return nil
	}
	if err != nil {
		return err
	}
	c := chain[0].principal.Capability
	if c.Team != string(team) || c.Billing == nil {
		return authorityError(apierr.IntegrityFailure, "invalid runner award")
	}
	key := c.Billing.ReservationID
	for _, ancestor := range chain[1:] {
		parent := ancestor.principal.Capability
		if c.Billing.ExecutorClass == "runner" && registeredClaimant(parent) || c.Billing.ExecutorClass == "local" && parent.Subject.Kind == capability.SubjectLocal {
			key = parent.Prefix
			break
		}
		if c.Billing.ExecutorClass == TierHere && parent.Purpose == capability.PurposeHere && parent.Billing != nil {
			key = parent.Billing.ReservationID
			break
		}
	}
	var at sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT last_used_at FROM capabilities WHERE team=? AND prefix=?`, team, prefix).Scan(&at); err != nil {
		return err
	}
	seen := c.IssuedAt
	if at.Valid {
		seen = time.Unix(0, at.Int64).UTC()
	}
	if seen.After(tx.time) {
		return authorityError(apierr.IntegrityFailure, "runner observation is in the future")
	}
	i, found := indices[key]
	if !found {
		location := "local"
		if c.Billing.ExecutorClass == TierCloud {
			location = TierCloud
		}
		i, indices[key] = len(out.Runners), len(out.Runners)
		out.Runners = append(out.Runners, capability.RunnerObservation{Team: string(team), Runner: nodeproto.RunnerIdentity{Name: c.Binding.Holder, Type: c.Billing.ExecutorClass}, Location: location, ActiveJobs: []string{}})
	}
	row := &out.Runners[i]
	if seen.After(row.LastSeen) {
		row.LastSeen = seen
	}
	job := c.Binding.RunID
	if c.Binding.NodeID != "" {
		job += "/" + c.Binding.NodeID
	}
	row.ActiveJobs = append(row.ActiveJobs, job)
	return nil
}

func observeUnitTx(ctx context.Context, tx *storeTx, prefix string) error {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false)
	if err != nil {
		return err
	}
	for i, ancestor := range chain {
		if i != 0 && !registeredClaimant(ancestor.principal.Capability) {
			continue
		}
		c := ancestor.principal.Capability
		result, err := tx.ExecContext(ctx, `UPDATE capabilities SET last_used_at=? WHERE prefix=? AND COALESCE(team,'')=?`, tx.time.UnixNano(), c.Prefix, c.Team)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "runner observation"); err != nil {
			return err
		}
	}
	return nil
}
