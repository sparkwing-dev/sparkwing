package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// safety: a claim hides the reason without writing the run row.
const runAttentionColumn = `COALESCE((SELECT n.attention_reason FROM nodes n
 WHERE n.team = runs.team AND n.run_id = runs.id AND n.ready_at IS NOT NULL AND n.claimed_by IS NULL
   AND n.` + nodeNotDone + ` AND n.attention_reason != ''
 ORDER BY n.node_id LIMIT 1), '')`

type runnerPoll struct {
	UnitDemand
	ObservedAt time.Time `json:"observed_at"`
}

// RecordRunnerPoll stores a live machine's advertised capacity and labels with the store clock.
func (o *Operator) RecordRunnerPoll(ctx context.Context, observation UnitDemand) error {
	if !validMatchingResources(observation.Capacity) || !validMatchingResources(observation.Available) {
		return authorityError(apierr.BadRequest, "invalid claim resources")
	}
	observation.Labels = match.SelfAsserted(observation.Labels)
	raw, err := json.Marshal(observation.Labels)
	if err != nil {
		return err
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: observation.CapabilityPrefix}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{ref}, ref.ID); err != nil {
			return err
		}
		chain, err := authorityChainTx(ctx, tx, ref, false)
		if err != nil {
			return err
		}
		principal := chain[0].principal
		facts, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
		if err != nil {
			return err
		}
		here, err := hereObserverTx(ctx, tx, principal)
		if err != nil {
			return err
		}
		c := principal.Capability
		if !facts.Observable && !here {
			return authorityError(apierr.Forbidden, "runner observations require a machine, Actions, local or launcher claimant")
		}
		body, err := json.Marshal(runnerPoll{UnitDemand: observation, ObservedAt: tx.time})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE capabilities SET advertised_labels_json=?,claim_observation_json=?,last_used_at=? WHERE COALESCE(team,'')=? AND prefix=?`, raw, body, tx.time.UnixNano(), c.Team, observation.CapabilityPrefix)
		return err
	})
}

// RegisteredAgent is a live agent credential of a team, online or not.
type RegisteredAgent struct {
	Name, CapabilityPrefix string
	// Profile combines enrollment and granted identity with last observed labels.
	Profile  match.Profile
	LastSeen time.Time
	poll     *runnerPoll
}

// ListRegisteredAgents reads this tenant's live machine claimants and observed labels.
func (t *Tenant) ListRegisteredAgents(ctx context.Context) ([]RegisteredAgent, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	out, err := registeredAgentsTx(ctx, tx, t.team)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func registeredAgentsTx(ctx context.Context, tx *storeTx, team Team) ([]RegisteredAgent, error) {
	agents, err := registeredAgentObservationsTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	var out []RegisteredAgent
	for _, observed := range agents {
		agent, err := registeredAgentTx(ctx, tx, team, observed)
		if unavailableRunner(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if agent != nil {
			out = append(out, *agent)
		}
	}
	return out, nil
}

func registeredAgentObservationsTx(ctx context.Context, tx *storeTx, team Team) ([]RegisteredAgent, error) {
	now := tx.time
	rows, err := tx.QueryContext(ctx, `SELECT prefix,advertised_labels_json,last_used_at,claim_observation_json FROM capabilities
 WHERE team=? AND subject_kind='machine' AND audience='controller' AND bind_kind='none'
 AND `+liveCredentialSQL("")+` ORDER BY prefix`, team, now.UnixNano(), now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var observations []RegisteredAgent
	for rows.Next() {
		var a RegisteredAgent
		var advertised, body []byte
		var used sql.NullInt64
		if err := rows.Scan(&a.CapabilityPrefix, &advertised, &used, &body); err != nil {
			return nil, err
		}
		if len(advertised) > 0 {
			if err := json.Unmarshal(advertised, &a.Profile.Labels); err != nil {
				return nil, fmt.Errorf("decode agent labels: %w", err)
			}
			a.Profile.Labels = match.SelfAsserted(a.Profile.Labels)
		}
		if used.Int64 > 0 {
			a.LastSeen = time.Unix(0, used.Int64).UTC()
		}
		if len(body) > 0 {
			a.poll = &runnerPoll{}
			if err := json.Unmarshal(body, a.poll); err != nil {
				return nil, err
			}
			if !validMatchingResources(a.poll.Capacity) || !validMatchingResources(a.poll.Available) || a.poll.ObservedAt.IsZero() || a.poll.ObservedAt.After(now) {
				return nil, authorityError(apierr.IntegrityFailure, "invalid runner capacity observation")
			}
		}
		observations = append(observations, a)
	}
	return observations, rows.Err()
}

func registeredAgentTx(ctx context.Context, tx *storeTx, team Team, observed RegisteredAgent) (*RegisteredAgent, error) {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: observed.CapabilityPrefix}, false)
	if err != nil {
		return nil, err
	}
	principal := chain[0].principal
	if principal.Capability.Team != string(team) || !registeredClaimant(principal.Capability) {
		return nil, nil
	}
	profile := match.Profile{}
	if tx.store.claimProfiles != nil {
		profile, _, err = tx.store.claimProfiles.ResolveClaimProfile(ctx, principal)
		if err != nil {
			return nil, err
		}
	}
	profile = authenticatedClaimProfile(principal.Capability, profile, false)
	profile.Labels = observed.Profile.Labels
	observed.Name, observed.Profile = profile.Name, profile
	return &observed, nil
}

func actionsClaimant(c capability.Capability) bool {
	return c.Subject.Kind == capability.SubjectMachine && c.Audience == capability.Controller && c.Binding.Kind == capability.BindingPush && (slices.Contains(c.Grants, capability.NodesClaim) || slices.Contains(c.Grants, capability.TriggersClaim))
}

func registeredClaimant(c capability.Capability) bool {
	return c.Subject.Kind == capability.SubjectMachine && c.Audience == capability.Controller && c.Binding.Kind == capability.BindingNone && (slices.Contains(c.Grants, capability.NodesClaim) || slices.Contains(c.Grants, capability.TriggersClaim) || launcherClaimant(c))
}

// WaitingNode is a ready node no agent holds, with what a claim of it checks.
type WaitingNode struct {
	RunID, NodeID string
	Team          Team
	Selector      []string
	Repo          match.Repository
	Request       match.Resources
	Attention     string
	AllowedPools  []string
}

// ListWaitingNodes lists up to limit ready nodes, ordered by run and node
// after the key after, that first became ready before readyBefore and that no
// agent has claimed. A controller-dispatched node's wait is explained by its
// run's unclaimed-work notice, so it is not listed.
func (o *Operator) ListWaitingNodes(ctx context.Context, readyBefore time.Time, after [2]string, limit int) ([]WaitingNode, error) {
	if limit <= 0 {
		return nil, errors.New("waiting node limit must be positive")
	}
	rows, err := o.s.query(ctx, `SELECT n.run_id,n.node_id,n.team,n.execution_json,n.attention_reason,
       r.repo_display,r.request_json,r.allowed_pools
  FROM nodes n JOIN runs r ON r.id=n.run_id AND r.team=n.team
 WHERE n.ready_at IS NOT NULL AND n.ready_at < ?
   AND n.claimed_by IS NULL AND n.status='pending' AND n.cancel_requested_at IS NULL
   AND r.status IN ('pending','running') AND r.cancel_requested_at IS NULL
   AND r.dispatch='host' AND r.intake_mode='execute'
   AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))
 ORDER BY n.run_id, n.node_id LIMIT ?`, readyBefore.UnixNano(), after[0], after[0], after[1], limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WaitingNode
	for rows.Next() {
		var n WaitingNode
		var execution, request, pools []byte
		if err := rows.Scan(&n.RunID, &n.NodeID, &n.Team, &execution, &n.Attention,
			&n.Repo.URL, &request, &pools); err != nil {
			return nil, err
		}
		var settings runspec.JobExecutionSettings
		if err := json.Unmarshal(execution, &settings); err != nil {
			return nil, fmt.Errorf("decode waiting job %s/%s: %w", n.RunID, n.NodeID, err)
		}
		n.Selector, n.Request = settings.Requires.Value, matchingResources(settings.Resources.Value)
		if !validMatchingResources(n.Request) {
			return nil, fmt.Errorf("invalid resources for waiting job %s/%s", n.RunID, n.NodeID)
		}
		if err := json.Unmarshal(pools, &n.AllowedPools); err != nil {
			return nil, fmt.Errorf("decode waiting run pools: %w", err)
		}
		var seal runspec.SealedRequest
		if err := json.Unmarshal(request, &seal); err != nil {
			return nil, fmt.Errorf("decode waiting run request: %w", err)
		}
		if n.Repo.URL == "" {
			n.Repo.URL = seal.Request.Git.Repository.Display
		}
		switch runner := seal.Request.Options.Runner.Value; runner.Kind {
		case runspec.RunnerName:
			n.Selector = append(n.Selector, "name="+runner.Value)
		case runspec.RunnerLabel:
			n.Selector = append(n.Selector, runner.Value)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NodeAttention is why one waiting node has no agent to claim it; an empty
// Reason clears the record.
type NodeAttention struct {
	Team          Team
	RunID, NodeID string
	Reason        string
}

// SetNodeAttention records the reasons. A node claimed or withdrawn from the
// queue since the sweep read it keeps none.
//
// safety: each reason is its own autocommitted single-row statement, so the
// sweep never holds one node's lock while it waits for another's, and run
// settlement, which locks a run's nodes in its own order, cannot deadlock
// against it. A reason is advisory, so one lost to an error is rewritten on
// the next pass.
func (o *Operator) SetNodeAttention(ctx context.Context, updates []NodeAttention) error {
	var errs []error
	for _, u := range updates {
		if _, err := o.s.exec(ctx, `UPDATE nodes SET attention_reason = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND ready_at IS NOT NULL AND claimed_by IS NULL
 AND status='pending' AND cancel_requested_at IS NULL
 AND EXISTS (SELECT 1 FROM runs r WHERE r.team=nodes.team AND r.id=nodes.run_id AND r.status IN ('pending','running') AND r.cancel_requested_at IS NULL)`,
			u.Reason, string(u.Team), u.RunID, u.NodeID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
