package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
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

// RecordRunnerPoll stores a live claimant's advertised labels and platform
// with the store clock. A poll that changes nothing is checked read-only and
// then only restamps the stored observation, with no team lock.
func (o *Operator) RecordRunnerPoll(ctx context.Context, observation UnitDemand) error {
	return o.recordRunnerPoll(ctx, observation, nil)
}

// safety: afterRead runs between the read-only check and the restamp, so a test can commit a concurrent write there.
func (o *Operator) recordRunnerPoll(ctx context.Context, observation UnitDemand, afterRead func()) error {
	if err := ValidateRunnerLabels(observation.Labels); err != nil {
		return err
	}
	if observation.MaxConcurrent < 0 {
		return authorityError(apierr.BadRequest, "runner concurrency must not be negative")
	}
	observation.Labels = match.SelfAsserted(observation.Labels)
	raw, err := json.Marshal(observation.Labels)
	if err != nil {
		return err
	}
	// safety: the fast path only saves work, so any failure in it except a refusal the locked write would repeat
	// falls through to the locked write, which is the complete check.
	team, seen, unchanged, err := o.runnerPollUnchanged(ctx, observation, raw)
	if refusal(err) {
		return err
	}
	if afterRead != nil {
		afterRead()
	}
	// perf: an unchanged poll still restamps, because placement holds judge liveness by the observation's time and
	// must count it from the runner's last poll, not its last change.
	if err == nil && unchanged {
		if stamped, err := o.stampRunnerPoll(ctx, observation, team, seen); err == nil && stamped {
			return nil
		}
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: observation.CapabilityPrefix}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{ref}, ref.ID); err != nil {
			return err
		}
		c, _, err := checkRunnerPollTx(ctx, tx, observation)
		if err != nil {
			return err
		}
		body, err := json.Marshal(runnerPoll{UnitDemand: observation, ObservedAt: tx.now(ctx)})
		if err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE capabilities SET advertised_labels_json=?,claim_observation_json=?,last_used_at=? WHERE COALESCE(team,'')=? AND prefix=?`, raw, body, tx.now(ctx).UnixNano(), c.Team, observation.CapabilityPrefix)
		return err
	})
}

func checkRunnerPollTx(ctx context.Context, tx *storeTx, observation UnitDemand) (leaf capability.Capability, root capability.Principal, err error) {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: observation.CapabilityPrefix}, false)
	if err != nil {
		return capability.Capability{}, capability.Principal{}, err
	}
	principal := chain[0].principal
	facts, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
	if err != nil {
		return capability.Capability{}, capability.Principal{}, err
	}
	here, err := hereObserverTx(ctx, tx, principal)
	if err != nil {
		return capability.Capability{}, capability.Principal{}, err
	}
	if err := checkRunnerLabelsTx(ctx, tx, chain, observation.Labels); err != nil {
		return capability.Capability{}, capability.Principal{}, err
	}
	if !facts.Observable && !here {
		return capability.Capability{}, capability.Principal{}, authorityError(hosterr.Forbidden, "runner observations require a machine, Actions, local or launcher claimant")
	}
	return principal.Capability, chain[len(chain)-1].principal, nil
}

// safety: for an unchanged observation of a live credential, the read-only snapshot applies the locked write's
// refusals, so skipping that write refuses exactly what it would; any other poll takes the locked write.
func (o *Operator) runnerPollUnchanged(ctx context.Context, observation UnitDemand, labels []byte) (_ string, _ runnerPollStamp, _ bool, err error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return "", runnerPollStamp{}, false, err
	}
	defer func() { err = errors.Join(err, tx.Rollback()) }()
	c, root, err := checkRunnerPollTx(ctx, tx, observation)
	if err != nil {
		return "", runnerPollStamp{}, false, err
	}
	var advertised, body, authority []byte
	var seen sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT advertised_labels_json,claim_observation_json,last_used_at FROM capabilities WHERE COALESCE(team,'')=? AND prefix=?`, c.Team, observation.CapabilityPrefix).Scan(&advertised, &body, &seen); err != nil {
		return "", runnerPollStamp{}, false, err
	}
	// safety: a chain rooted in an account session has no capability row to fence, so it takes the locked write.
	if root.Authority.Kind != capability.AuthorityCapability {
		return c.Team, runnerPollStamp{}, false, nil
	}
	// safety: allowed labels live on the chain root, which is a different row when a machine root delegated the poller.
	if err := tx.QueryRowContext(ctx, `SELECT authority_json FROM capabilities WHERE COALESCE(team,'')=? AND prefix=?`, root.Capability.Team, root.Authority.ID).Scan(&authority); err != nil {
		return "", runnerPollStamp{}, false, err
	}
	var stored runnerPoll
	if len(body) == 0 || !seen.Valid || !bytes.Equal(advertised, labels) || json.Unmarshal(body, &stored) != nil {
		return c.Team, runnerPollStamp{}, false, nil
	}
	want, err := json.Marshal(observation)
	if err != nil {
		return "", runnerPollStamp{}, false, err
	}
	have, err := json.Marshal(stored.UnitDemand)
	return c.Team, runnerPollStamp{lastUsed: seen.Int64, body: body, rootTeam: root.Capability.Team, rootPrefix: root.Authority.ID, rootAuthority: authority}, err == nil && bytes.Equal(want, have), err
}

// safety: a restamp must still find the stored observation and last-seen time the unchanged poll read.
type runnerPollStamp struct {
	lastUsed             int64
	body                 []byte
	rootTeam, rootPrefix string
	rootAuthority        []byte
}

// perf: an unchanged poll restamps its observation in one statement instead of the locked write.
//
// safety: the update applies only while the row holds the observation and last_used_at, and the chain root the
// authority, the snapshot read, so a concurrent poll, an award or a label change sends it to the locked write.
func (o *Operator) stampRunnerPoll(ctx context.Context, observation UnitDemand, team string, seen runnerPollStamp) (bool, error) {
	var stamped bool
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now(ctx)
		body, err := json.Marshal(runnerPoll{UnitDemand: observation, ObservedAt: now})
		if err != nil {
			return err
		}
		// safety: an ancestor's revocation is not fenced here: the read-only check walked the chain just before,
		// and a stamp that slips past one only advances a timestamp that authorization ignores.
		result, err := tx.execContext(ctx, `UPDATE capabilities SET claim_observation_json=?,last_used_at=? WHERE COALESCE(team,'')=? AND prefix=? AND last_used_at=? AND claim_observation_json=? AND `+liveCredentialSQL("")+` AND EXISTS (SELECT 1 FROM capabilities root WHERE COALESCE(root.team,'')=? AND root.prefix=? AND root.authority_json=?)`, body, now.UnixNano(), team, observation.CapabilityPrefix, seen.lastUsed, seen.body, now.UnixNano(), now.UnixNano(), seen.rootTeam, seen.rootPrefix, seen.rootAuthority)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		stamped = rows == 1
		return err
	})
	return stamped, err
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
	now := tx.now(ctx)
	rows, err := tx.queryContext(ctx, `SELECT prefix,advertised_labels_json,last_used_at,claim_observation_json FROM capabilities
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
			if a.poll.ObservedAt.IsZero() || a.poll.ObservedAt.After(now) {
				return nil, authorityError(hosterr.IntegrityFailure, "invalid runner label observation")
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
		profile, err = tx.store.claimProfiles.ResolveClaimProfile(ctx, principal)
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
	q := claimSQL{dialect: o.s.dialect}
	rows, err := o.s.query(ctx, `SELECT n.run_id,n.node_id,n.team,n.execution_json,n.attention_reason,
       r.repo_display,r.request_json,`+q.allowedPools("r.team", q.at(q.blob("r.request_json"), "provenance"))+`
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
		n.Selector = settings.Requires.Value
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

func consumeClaimSlotTx(ctx context.Context, tx *storeTx, prefix, team string) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT claim_observation_json FROM capabilities WHERE prefix=? AND COALESCE(team,'')=?`, prefix, team).Scan(&raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var poll runnerPoll
	if err := json.Unmarshal(raw, &poll); err != nil {
		return err
	}
	slots := 1
	if poll.FreeSlots != nil {
		slots = *poll.FreeSlots
	}
	slots = max(0, slots-1)
	poll.FreeSlots = &slots
	raw, err := json.Marshal(poll)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `UPDATE capabilities SET claim_observation_json=? WHERE prefix=? AND COALESCE(team,'')=?`, raw, prefix, team)
	return err
}
