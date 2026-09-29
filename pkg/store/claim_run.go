package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StartClaimExecution records that tok's claim is about to run pipeline
// code, and returns the spec hash the run's accepted plan holds for the node
// (empty for the planning node). It is idempotent, and it refuses a claim
// that is not live or whose run is being cancelled, so no pipeline code
// starts after either. From this point the claim is issued no source
// credential.
func (s *Store) StartClaimExecution(ctx context.Context, tok ClaimToken, now time.Time) (specHash string, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return "", err
	}
	if err := fenceSensitiveClaimTx(ctx, tx, tok, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes
   SET execution_started_at = COALESCE(execution_started_at, ?), started_at = COALESCE(started_at, ?)
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`,
		now.UnixNano(), now.UnixNano(), string(tok.Team), tok.RunID, tok.NodeID, tok.Generation); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT spec_hash FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(tok.Team), tok.RunID, tok.NodeID).Scan(&specHash); err != nil {
		return "", err
	}
	return specHash, tx.Commit()
}

// ClaimBeat is what one claim heartbeat found.
type ClaimBeat struct {
	// Cancel reports a cancel request on the claim's run: the pod stops its
	// pipeline and reports the attempt.
	Cancel bool
	// Charge is the billing the beat settled; Charge.Cancel means the team's
	// balance ran out and the claim was not renewed.
	Charge CreditChargeResult
}

// HeartbeatClaim renews tok's live claim for lease and bills the seconds its
// node has run since the last beat. The first beat opens the node's billing.
// A claim that is not live, or whose team can no longer pay, is refused with
// [ErrLockHeld] and not renewed; a cancel request renews it, so the pod can
// report, and says so in the result.
func (s *Store) HeartbeatClaim(ctx context.Context, tok ClaimToken, lease time.Duration, now time.Time) (beat ClaimBeat, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ClaimBeat{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return ClaimBeat{}, err
	}
	var prefix string
	var anchor int64
	err = tx.QueryRowContext(ctx, `SELECT claim_token_prefix, credit_charged_through FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ? AND `+nodeNotDone+` AND `+nodeClaimLiveSQL("")+tx.forUpdate(),
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, now.UnixNano()).Scan(&prefix, &anchor)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimBeat{}, ErrLockHeld
	}
	if err != nil {
		return ClaimBeat{}, err
	}
	if anchor != 0 {
		charged, err := s.chargeNodeTx(ctx, tx, tok.RunID, tok.NodeID, prefix, now, false)
		if err != nil {
			return ClaimBeat{}, err
		}
		if beat.Charge = charged.CreditChargeResult; beat.Charge.Cancel {
			return beat, errors.Join(tx.Commit(), ErrLockHeld)
		}
	}
	if beat.Cancel, err = claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID); err != nil {
		return ClaimBeat{}, err
	}
	// safety: the ledger can hold the transaction past the lease, so the
	// renewal is judged against a fresh clock, as a runner's renewal is.
	renewAt := time.Now()
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET lease_expires_at = ?, last_heartbeat = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ? AND `+nodeClaimLiveSQL(""),
		renewAt.Add(clampNodeLease(lease)).UnixNano(), renewAt.UnixNano(),
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, renewAt.UnixNano())
	if err != nil {
		return ClaimBeat{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ClaimBeat{}, errors.Join(err, ErrLockHeld)
	}
	return beat, tx.Commit()
}

// ErrSecretUndeclared refuses a claim a secret its run's accepted plan does
// not declare.
var ErrSecretUndeclared = errors.New("store: the run's pipeline does not declare this secret")

// ReleaseClaimSecret returns the secret name to tok's live work claim, looked
// up for the run's pipeline, when the run's accepted plan declares it. It
// records the release as a secret_released event on the node under the
// sensitive fence, so a release after the claim ended or its run was
// cancelled is refused, and a planning claim is never answered.
func (t *Tenant) ReleaseClaimSecret(ctx context.Context, tok ClaimToken, name string, now time.Time) (_ *Secret, err error) {
	if tok.Team != t.team || tok.Kind != ClaimTokenWork {
		return nil, ErrClaimNotLive
	}
	var pipeline string
	var plan []byte
	if err := t.s.queryRow(ctx, `SELECT pipeline, plan_json FROM runs WHERE team = ? AND id = ?`,
		string(t.team), tok.RunID).Scan(&pipeline, &plan); err != nil {
		return nil, err
	}
	var doc struct {
		Secrets []struct {
			Name string `json:"name"`
		} `json:"secrets"`
	}
	if len(plan) > 0 {
		if err := json.Unmarshal(plan, &doc); err != nil {
			return nil, fmt.Errorf("read the run's plan: %w", err)
		}
	}
	declared := false
	for _, d := range doc.Secrets {
		declared = declared || d.Name == name
	}
	if !declared {
		return nil, ErrSecretUndeclared
	}
	sec, err := t.GetSecretForRun(name, pipeline)
	if err != nil {
		return nil, err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return nil, err
	}
	if err := fenceSensitiveClaimTx(ctx, tx, tok, now); err != nil {
		return nil, err
	}
	if _, err := appendEventTx(ctx, tx, tok.RunID, tok.NodeID, "secret_released",
		map[string]any{"name": name, "generation": tok.Generation}, now); err != nil {
		return nil, err
	}
	return sec, tx.Commit()
}

// ErrSlotUndeclared refuses a claim a concurrency slot its node's accepted
// plan does not declare with that key, policy, capacity and cost.
var ErrSlotUndeclared = errors.New("store: the run's accepted plan declares no such slot for this node")

// CheckClaimSlot allows tok's work claim to acquire key only as its node's
// accepted plan declares it: the node's concurrency group, under the key its
// scope names, with the declared policy, capacity and cost, or the node's
// memoization under its coalescing key.
func (s *Store) CheckClaimSlot(ctx context.Context, tok ClaimToken, key, policy string, capacity, cost int) error {
	var plan []byte
	if err := s.queryRow(ctx, `SELECT plan_json FROM runs WHERE team = ? AND id = ?`,
		string(tok.Team), tok.RunID).Scan(&plan); err != nil {
		return err
	}
	var doc struct {
		Nodes []struct {
			ID        string             `json:"id"`
			Modifiers submittedModifiers `json:"modifiers"`
		} `json:"nodes"`
	}
	if len(plan) == 0 || json.Unmarshal(plan, &doc) != nil {
		return ErrSlotUndeclared
	}
	for _, n := range doc.Nodes {
		if n.ID != tok.NodeID {
			continue
		}
		m := n.Modifiers
		memo := m.Cache && strings.HasPrefix(key, memoSlotPrefix) && policy == OnLimitCoalesce && capacity == 1 && cost == 1
		group := m.ConcGroup != "" && key == declaredSlotKey(m, tok.RunID) && policy == m.ConcOnLimit &&
			capacity == m.ConcCapacity && cost == m.ConcCost
		if memo || group {
			return nil
		}
	}
	return ErrSlotUndeclared
}

const memoSlotPrefix = "memo:"

// safety: the SDK's key for a node's group: a run-scoped group is qualified
// by its run, so a claim can name no other run's run-scoped key.
func declaredSlotKey(m submittedModifiers, runID string) string {
	if m.ConcScope == "run" {
		return "r:" + strconv.Itoa(len(runID)) + ":" + runID + m.ConcGroup
	}
	return "g:" + m.ConcGroup
}
