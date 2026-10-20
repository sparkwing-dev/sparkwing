package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// DesiredRunEffect is a committed snapshot for delivery; Payload comes from the run, never an executor.
type DesiredRunEffect struct {
	Team         Team
	RunID        string
	Effect       Effect
	Phase        EffectPhase
	State        RunState
	Reason       string
	Payload      json.RawMessage
	DesiredRev   int64
	DeliveredRev int64
	Attempts     int64
	NextAt       time.Time
	ObservedAt   time.Time
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
	return o.dueRunEffects(ctx, lease, "", limit)
}

// DueProfileFolds selects terminal profile work without starvation by other effect types.
func (o *Operator) DueProfileFolds(ctx context.Context, lease SingletonLease, limit int) ([]DesiredRunEffect, error) {
	return o.dueRunEffects(ctx, lease, EffectProfileFold, limit)
}

// DueGitHubChecks selects GitHub work without starvation by other effect types.
func (o *Operator) DueGitHubChecks(ctx context.Context, lease SingletonLease, limit int) ([]DesiredRunEffect, error) {
	return o.dueRunEffects(ctx, lease, EffectGitHubCheck, limit)
}

func (o *Operator) dueRunEffects(ctx context.Context, lease SingletonLease, effect Effect, limit int) ([]DesiredRunEffect, error) {
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
	observedAt := tx.now()
	rows, err := tx.QueryContext(ctx, `SELECT e.team,e.run_id,e.effect,e.desired_state,e.desired_rev,e.delivered_rev,e.attempts,e.next_at,
r.pipeline,r.git_sha,r.git_branch,r.error,
(SELECT v.payload FROM events v WHERE v.team=r.team AND v.run_id=r.id AND v.node_id='' AND v.state_seq=r.state_seq AND v.kind IN ('run_success','run_failed','run_cancelled') ORDER BY v.seq DESC LIMIT 1) FROM run_effects e JOIN runs r ON r.team = e.team AND r.id = e.run_id
WHERE e.delivered_rev < e.desired_rev AND e.next_at <= ? AND (?='' OR e.effect=?) ORDER BY e.next_at,e.team,e.run_id,e.effect LIMIT ?`, observedAt.UnixNano(), effect, effect, limit)
	if err != nil {
		return nil, err
	}
	var due []DesiredRunEffect
	for rows.Next() {
		var effect DesiredRunEffect
		var next int64
		var pipeline, sha, branch, runError string
		var event []byte
		if err := rows.Scan(&effect.Team, &effect.RunID, &effect.Effect, &effect.State, &effect.DesiredRev, &effect.DeliveredRev, &effect.Attempts, &next, &pipeline, &sha, &branch, &runError, &event); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		var terminal struct {
			Reason string `json:"reason"`
		}
		if len(event) > 0 {
			if err := json.Unmarshal(event, &terminal); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
		}
		effect.Reason = terminal.Reason
		effect.NextAt = time.Unix(0, next)
		effect.ObservedAt = observedAt
		effect.Phase = effectPhase(effect.State)
		effect.Payload, err = json.Marshal(struct {
			Version  int      `json:"version"`
			RunID    string   `json:"run_id"`
			Pipeline string   `json:"pipeline"`
			State    RunState `json:"state"`
			SHA      string   `json:"git_sha"`
			Branch   string   `json:"git_branch"`
			Error    string   `json:"error"`
			Reason   string   `json:"reason,omitempty"`
		}{1, effect.RunID, pipeline, effect.State, sha, branch, runError, effect.Reason})
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
	return t.deliverRunEffect(ctx, delivery, nil, nil)
}

// RejectRunEffect records one failed attempt and consumes only the invalid revision.
func (t *Tenant) RejectRunEffect(ctx context.Context, delivery EffectDelivery) error {
	return t.deliverRunEffect(ctx, delivery, nil, func(tx *storeTx) error {
		_, err := tx.ExecContext(ctx, `UPDATE run_effects SET attempts=attempts+1 WHERE team=? AND run_id=? AND effect=?`, t.team, delivery.RunID, delivery.Effect)
		return err
	})
}

// RetryRunEffect records a failed delivery without overwriting a later desired state or its due work.
func (t *Tenant) RetryRunEffect(ctx context.Context, delivery EffectDelivery, nextAt time.Time) error {
	if nextAt.IsZero() {
		return fmt.Errorf("effect retry time is required")
	}
	return t.deliverRunEffect(ctx, delivery, &nextAt, nil)
}

func (t *Tenant) deliverRunEffect(ctx context.Context, delivery EffectDelivery, retryAt *time.Time, apply func(*storeTx) error) error {
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
		if apply != nil {
			if err := apply(tx); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE run_effects SET delivered_rev = ?, delivered_at = ? WHERE team = ? AND run_id = ? AND effect = ?`, delivery.Revision, tx.now().UnixNano(), t.team, delivery.RunID, delivery.Effect)
	}
	if err != nil {
		return err
	}
	if err := assertSingletonLeaseTx(ctx, tx, delivery.Lease, "completion", tx.now()); err != nil {
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
	for _, effect := range []Effect{EffectGitHubCheck, EffectMetrics, EffectProfileFold, EffectNotifyParent} {
		if effect != EffectGitHubCheck && phase != EffectTerminal {
			continue
		}
		var prior RunState
		err := tx.QueryRowContext(ctx, `SELECT desired_state FROM run_effects WHERE team = ? AND run_id = ? AND effect = ?`+tx.forUpdate(), team, runID, effect).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES (?,?,?,?,1,?)`, team, runID, effect, state, now.UnixNano())
		} else if err == nil && phase != EffectPending && effectPhase(prior) != phase && effectPhase(prior) != EffectTerminal {
			_, err = tx.ExecContext(ctx, `UPDATE run_effects SET desired_state = ?, desired_rev = desired_rev + 1, next_at = ? WHERE team = ? AND run_id = ? AND effect = ?`, state, now.UnixNano(), team, runID, effect)
		} else if err == nil && prior != state && effectPhase(prior) == EffectTerminal && phase == EffectTerminal {
			return fmt.Errorf("terminal completion outcome cannot change from %s to %s", prior, state)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ApplyProfileFold commits terminal profile observations with their exact delivered revision.
func (t *Tenant) ApplyProfileFold(ctx context.Context, delivery EffectDelivery, observations map[string]ProfileObservation) (bool, error) {
	if delivery.Effect != EffectProfileFold {
		return false, fmt.Errorf("profile fold requires its own effect")
	}
	applied := false
	err := t.deliverRunEffect(ctx, delivery, nil, func(tx *storeTx) error {
		run, err := readRunTx(ctx, tx, t.team, delivery.RunID)
		if err != nil {
			return err
		}
		if effectPhase(run.State) != EffectTerminal {
			return fmt.Errorf("profile fold requires a terminal run")
		}
		ids := make([]string, 0, len(observations))
		for id := range observations {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if err := t.recordProfileObservationTx(ctx, tx, run.Pipeline, id, observations[id]); err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	return applied && err == nil, err
}
