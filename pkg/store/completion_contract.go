package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Effect identifies one revisioned desired-state row per team and run.
type Effect string

// Completion effects replace every finish-handler side effect.
const (
	EffectGitHubCheck  Effect = "github_check"
	EffectMetrics      Effect = "metrics"
	EffectProfileFold  Effect = "profile_fold"
	EffectNotifyParent Effect = "notify_parent"
)

// EffectPhase is ranked independently of the terminal outcome so a late pending write cannot regress it.
type EffectPhase string

// Effect phases only advance; terminal outcome changes are illegal run transitions.
const (
	EffectPending  EffectPhase = "pending"
	EffectRunning  EffectPhase = "running"
	EffectTerminal EffectPhase = "terminal"
)

// EffectPolicy controls delivery phases and external idempotency keys.
type EffectPolicy struct {
	Effect       Effect
	TerminalOnly bool
	Idempotency  string
}

// CompletionEffects is the entire effect registry consumed by the elected reconciler.
func CompletionEffects() []EffectPolicy {
	return []EffectPolicy{
		{EffectGitHubCheck, false, "team/run/effect/revision; serialized report lock"},
		{EffectMetrics, true, "team/run/effect"},
		{EffectProfileFold, true, "team/run/effect"},
		{EffectNotifyParent, true, "team/run/effect"},
	}
}

// EffectPhaseRanks supplies the monotone desired-state order.
func EffectPhaseRanks() map[EffectPhase]int {
	return map[EffectPhase]int{EffectPending: 0, EffectRunning: 1, EffectTerminal: 2}
}

// DesiredRunEffect is a committed snapshot for delivery; Payload comes from the run, never an executor.
type DesiredRunEffect struct {
	Team         Team
	RunID        string
	Effect       Effect
	Phase        EffectPhase
	State        RunState
	Payload      json.RawMessage
	DesiredRev   int64
	DeliveredRev int64
	Attempts     int64
	NextAt       time.Time
}

// EffectDelivery acknowledges only the revision actually sent, leaving a newer desired revision due.
type EffectDelivery struct {
	RunID    string
	Effect   Effect
	Revision int64
	Lease    SingletonLease
}

// DueRunEffects reads revisions under the completion singleton lease; expiry stops further delivery acknowledgements.
func (o *Operator) DueRunEffects(ctx context.Context, lease SingletonLease, limit int) ([]DesiredRunEffect, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("effect limit must be positive")
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, lease, "completion", tx.now()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.team,e.run_id,e.effect,e.desired_state,e.desired_rev,e.delivered_rev,e.attempts,e.next_at,
r.pipeline,r.git_sha,r.git_branch,r.error FROM run_effects e JOIN runs r ON r.team = e.team AND r.id = e.run_id
WHERE e.delivered_rev < e.desired_rev AND e.next_at <= ? ORDER BY e.next_at,e.team,e.run_id,e.effect LIMIT ?`, tx.now().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	var due []DesiredRunEffect
	for rows.Next() {
		var effect DesiredRunEffect
		var next int64
		var pipeline, sha, branch, runError string
		if err := rows.Scan(&effect.Team, &effect.RunID, &effect.Effect, &effect.State, &effect.DesiredRev, &effect.DeliveredRev, &effect.Attempts, &next, &pipeline, &sha, &branch, &runError); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		effect.NextAt = time.Unix(0, next)
		effect.Phase = effectPhase(effect.State)
		effect.Payload, err = json.Marshal(struct {
			Version  int      `json:"version"`
			RunID    string   `json:"run_id"`
			Pipeline string   `json:"pipeline"`
			State    RunState `json:"state"`
			SHA      string   `json:"git_sha"`
			Branch   string   `json:"git_branch"`
			Error    string   `json:"error"`
		}{1, effect.RunID, pipeline, effect.State, sha, branch, runError})
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		due = append(due, effect)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return due, tx.Commit()
}

// AcknowledgeRunEffect advances delivered_rev only to the delivered revision, never to a newly observed desired_rev.
func (t *Tenant) AcknowledgeRunEffect(ctx context.Context, delivery EffectDelivery) error {
	return t.deliverRunEffect(ctx, delivery, nil)
}

// RetryRunEffect records a failed delivery without overwriting a later desired state or its due work.
func (t *Tenant) RetryRunEffect(ctx context.Context, delivery EffectDelivery, nextAt time.Time) error {
	if nextAt.IsZero() {
		return fmt.Errorf("effect retry time is required")
	}
	return t.deliverRunEffect(ctx, delivery, &nextAt)
}

func (t *Tenant) deliverRunEffect(ctx context.Context, delivery EffectDelivery, retryAt *time.Time) error {
	if delivery.RunID == "" || delivery.Revision < 1 {
		return fmt.Errorf("invalid effect delivery")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, delivery.Lease, "completion", tx.now()); err != nil {
		return err
	}
	var desired, delivered int64
	err = tx.QueryRowContext(ctx, `SELECT desired_rev,delivered_rev FROM run_effects WHERE team = ? AND run_id = ? AND effect = ?`+tx.forUpdate(), t.team, delivery.RunID, delivery.Effect).Scan(&desired, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if delivery.Revision > desired {
		return fmt.Errorf("effect revision %d exceeds desired revision %d", delivery.Revision, desired)
	}
	if delivery.Revision <= delivered {
		return tx.Commit()
	}
	if retryAt != nil {
		_, err = tx.ExecContext(ctx, `UPDATE run_effects SET attempts = attempts + 1, next_at = CASE WHEN desired_rev = ? THEN ? ELSE next_at END WHERE team = ? AND run_id = ? AND effect = ?`, delivery.Revision, retryAt.UnixNano(), t.team, delivery.RunID, delivery.Effect)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE run_effects SET delivered_rev = ?, delivered_at = ? WHERE team = ? AND run_id = ? AND effect = ?`, delivery.Revision, tx.now().UnixNano(), t.team, delivery.RunID, delivery.Effect)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func effectPhase(state RunState) EffectPhase {
	switch state {
	case RunPending:
		return EffectPending
	case RunRunning:
		return EffectRunning
	default:
		return EffectTerminal
	}
}

func writeRunEffectsTx(ctx context.Context, tx *storeTx, team Team, runID string, state RunState, now time.Time) error {
	if state != RunPending && state != RunRunning && state != RunSuccess && state != RunFailed && state != RunCancelled {
		return fmt.Errorf("invalid effect state %q", state)
	}
	phase := effectPhase(state)
	for _, policy := range CompletionEffects() {
		if policy.TerminalOnly && phase != EffectTerminal {
			continue
		}
		var prior RunState
		err := tx.QueryRowContext(ctx, `SELECT desired_state FROM run_effects WHERE team = ? AND run_id = ? AND effect = ?`+tx.forUpdate(), team, runID, policy.Effect).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES (?,?,?,?,1,?)`, team, runID, policy.Effect, state, now.UnixNano())
		} else if err == nil && prior != state && EffectPhaseRanks()[phase] > EffectPhaseRanks()[effectPhase(prior)] {
			_, err = tx.ExecContext(ctx, `UPDATE run_effects SET desired_state = ?, desired_rev = desired_rev + 1, next_at = ? WHERE team = ? AND run_id = ? AND effect = ?`, state, now.UnixNano(), team, runID, policy.Effect)
		} else if err == nil && prior != state && effectPhase(prior) == EffectTerminal && phase == EffectTerminal {
			return fmt.Errorf("terminal completion outcome cannot change from %s to %s", prior, state)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
