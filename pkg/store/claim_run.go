package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	n, ok, err := s.declaredNode(ctx, tok.Team, tok.RunID, tok.NodeID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSlotUndeclared
	}
	m := n.Modifiers
	prefix, err := s.claimMemoPrefix(ctx, tok)
	if err != nil {
		return err
	}
	memo := m.Cache && strings.HasPrefix(key, prefix) && len(key) > len(prefix) && policy == OnLimitCoalesce && capacity == 1 && cost == 1
	group := m.ConcGroup != "" && key == declaredSlotKey(m, tok.RunID) && policy == m.ConcOnLimit &&
		capacity == m.ConcCapacity && cost == m.ConcCost
	if memo || group {
		return nil
	}
	return ErrSlotUndeclared
}

type declaredPlanNode struct {
	ID           string             `json:"id"`
	Modifiers    submittedModifiers `json:"modifiers"`
	PipelineRefs []PlanPipelineRef  `json:"pipeline_refs"`
}

// safety: a run with no stored plan, or a plan without the node, declares
// nothing, so a claim on it reaches no slot or input.
func (s *Store) declaredNode(ctx context.Context, team Team, runID, nodeID string) (declaredPlanNode, bool, error) {
	var plan []byte
	err := s.queryRow(ctx, `SELECT plan_json FROM runs WHERE team = ? AND id = ?`, string(team), runID).Scan(&plan)
	if errors.Is(err, sql.ErrNoRows) {
		return declaredPlanNode{}, false, nil
	}
	if err != nil {
		return declaredPlanNode{}, false, err
	}
	var doc struct {
		Nodes []declaredPlanNode `json:"nodes"`
	}
	if len(plan) == 0 {
		return declaredPlanNode{}, false, nil
	}
	if err := json.Unmarshal(plan, &doc); err != nil {
		return declaredPlanNode{}, false, fmt.Errorf("read run %s's plan: %w", runID, err)
	}
	for _, n := range doc.Nodes {
		if n.ID == nodeID {
			return n, true, nil
		}
	}
	return declaredPlanNode{}, false, nil
}

// PlanPipelineRef is one other pipeline's node a plan node declares it reads
// the newest successful run of, as RefToLastRun does.
type PlanPipelineRef struct {
	Pipeline string `json:"pipeline"`
	Node     string `json:"node"`
}

// ClaimInputKind names where a claimed node's input from another run comes
// from.
type ClaimInputKind string

// The inputs a claimed node reads from another run.
const (
	ClaimInputCached    ClaimInputKind = "cached"
	ClaimInputCoalesced ClaimInputKind = "coalesced"
	ClaimInputLastRun   ClaimInputKind = "last_run"
)

// ClaimInputRequest names the reference a work claim's node reads through: its
// memoized result by the cache key hash, for a cache hit or for the leader it
// coalesced onto, or another pipeline's newest successful run by the pipeline
// and node its plan declares. It never names the run it reads.
type ClaimInputRequest struct {
	Kind         ClaimInputKind `json:"kind"`
	Key          string         `json:"key,omitempty"`
	CacheKeyHash string         `json:"cache_key_hash,omitempty"`
	Pipeline     string         `json:"pipeline,omitempty"`
	Node         string         `json:"node,omitempty"`
	MaxAgeMS     int64          `json:"max_age_ms,omitempty"`
}

// ClaimInput is the finished output a claim's input request resolved to and
// the run and node it came from.
type ClaimInput struct {
	RunID      string     `json:"run_id"`
	NodeID     string     `json:"node_id"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Output grants a read of the node's output; an empty URL means it
	// recorded none.
	Output OutputReadGrant `json:"output"`
}

// ErrInputUndeclared refuses a claim an input its node's accepted plan does
// not declare.
var ErrInputUndeclared = errors.New("store: the run's accepted plan declares no such input for this node")

// ResolveClaimInput resolves the output tok's node reads from another run of
// its team. The store picks the source: for the node's declared memoization,
// the leader its own coalesce waiter names or else the cache entry its leader
// wrote; for a pipeline reference the node declares, that pipeline's newest
// successful run. No other run is reachable through it.
func (s *Store) ResolveClaimInput(ctx context.Context, tok ClaimToken, req ClaimInputRequest, now time.Time) (ClaimInput, error) {
	n, ok, err := s.declaredNode(ctx, tok.Team, tok.RunID, tok.NodeID)
	if err != nil {
		return ClaimInput{}, err
	}
	if !ok {
		return ClaimInput{}, ErrInputUndeclared
	}
	var runID, nodeID string
	switch req.Kind {
	case ClaimInputCached, ClaimInputCoalesced:
		prefix, err := s.claimMemoPrefix(ctx, tok)
		if err != nil {
			return ClaimInput{}, err
		}
		if !n.Modifiers.Cache || req.CacheKeyHash == "" || req.Key != prefix+req.CacheKeyHash {
			return ClaimInput{}, ErrInputUndeclared
		}
		found := false
		if req.Kind == ClaimInputCoalesced {
			// safety: the leader comes from this node's own waiter row, never
			// from the claim; once the leader's release drains the row, the
			// cache entry it wrote in the same transaction names it instead.
			err := s.queryRow(ctx, `SELECT leader_run_id, leader_node_id FROM concurrency_waiters
 WHERE team = ? AND key = ? AND run_id = ? AND node_id = ? AND policy = ?`,
				string(tok.Team), req.Key, tok.RunID, tok.NodeID, OnLimitCoalesce).Scan(&runID, &nodeID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return ClaimInput{}, err
			}
			found = err == nil && runID != ""
		}
		if !found {
			var expires int64
			err := s.queryRow(ctx, `SELECT origin_run_id, origin_node_id, expires_at FROM concurrency_cache
 WHERE team = ? AND key = ? AND cache_key_hash = ?`, string(tok.Team), req.Key, req.CacheKeyHash).Scan(&runID, &nodeID, &expires)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && expires <= now.UnixNano()) {
				return ClaimInput{}, notFound("cache entry", req.Key)
			}
			if err != nil {
				return ClaimInput{}, err
			}
		}
	case ClaimInputLastRun:
		if !slices.Contains(n.PipelineRefs, PlanPipelineRef{Pipeline: req.Pipeline, Node: req.Node}) {
			return ClaimInput{}, ErrInputUndeclared
		}
		run, err := s.getLatestRun(ctx, oneTeam(tok.Team), req.Pipeline, []string{"success"},
			time.Duration(max(req.MaxAgeMS, 0))*time.Millisecond)
		if err != nil {
			return ClaimInput{}, err
		}
		runID, nodeID = run.ID, req.Node
	default:
		return ClaimInput{}, fmt.Errorf("%w: input kind %q is not cached, coalesced or last_run", ErrInvalidInput, req.Kind)
	}
	if req.Kind != ClaimInputLastRun {
		if err := s.sameMemoIdentity(ctx, tok, runID, nodeID); err != nil {
			return ClaimInput{}, err
		}
	}
	in := ClaimInput{RunID: runID, NodeID: nodeID}
	var status, outcome string
	var finished sql.NullInt64
	err = s.queryRow(ctx, `SELECT status, outcome, finished_at FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`, string(tok.Team), runID, nodeID).Scan(&status, &outcome, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimInput{}, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return ClaimInput{}, err
	}
	if status != "done" || (req.Kind != ClaimInputLastRun && outcome != "success") {
		return ClaimInput{}, notFound("finished output", runID+"/"+nodeID)
	}
	if finished.Valid && finished.Int64 > 0 {
		t := time.Unix(0, finished.Int64)
		in.FinishedAt = &t
	}
	return in, nil
}

const memoSlotPrefix = "memo:"

// ClaimMemoKey is the concurrency key a controller-dispatched node memoizes
// under: its repository, pipeline and node, then the content hash, so a claim
// reaches only its own node's memoized results.
func ClaimMemoKey(repo, pipeline, node, hash string) string {
	return memoSlotPrefix + "c/" + lengthPrefixed(repo) + lengthPrefixed(pipeline) + lengthPrefixed(node) + hash
}

// ClaimRepoSlug names a run's repository in [ClaimMemoKey].
func ClaimRepoSlug(owner, repo string) string {
	return strings.ToLower(owner) + "/" + strings.ToLower(repo)
}

func lengthPrefixed(s string) string { return strconv.Itoa(len(s)) + ":" + s }

// safety: the prefix is built from the claim's own run and node, so no field
// of the request can move a claim into another node's memoized results.
func (s *Store) claimMemoPrefix(ctx context.Context, tok ClaimToken) (string, error) {
	var pipeline, owner, repo string
	if err := s.queryRow(ctx, `SELECT pipeline, COALESCE(github_owner, ''), COALESCE(github_repo, '') FROM runs WHERE team = ? AND id = ?`,
		string(tok.Team), tok.RunID).Scan(&pipeline, &owner, &repo); err != nil {
		return "", err
	}
	return ClaimMemoKey(ClaimRepoSlug(owner, repo), pipeline, tok.NodeID, ""), nil
}

// safety: the SDK's key for a node's group: a run-scoped group is qualified
// by its run, so a claim can name no other run's run-scoped key.
func declaredSlotKey(m submittedModifiers, runID string) string {
	if m.ConcScope == "run" {
		return "r:" + strconv.Itoa(len(runID)) + ":" + runID + m.ConcGroup
	}
	return "g:" + m.ConcGroup
}

// safety: a memoized result reaches a claim only from its own team,
// repository, pipeline and node, whoever wrote the entry under its key, so a
// hash known from elsewhere in the team discloses nothing.
func (s *Store) sameMemoIdentity(ctx context.Context, tok ClaimToken, runID, nodeID string) error {
	identity := func(id string) (pipeline string, repoID int64, err error) {
		err = s.queryRow(ctx, `SELECT r.pipeline, COALESCE(t.github_repo_id, 0) FROM runs r
  JOIN triggers t ON t.team = r.team AND t.id = r.id
 WHERE r.team = ? AND r.id = ?`, string(tok.Team), id).Scan(&pipeline, &repoID)
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrInputUndeclared
		}
		return pipeline, repoID, err
	}
	ownPipeline, ownRepo, err := identity(tok.RunID)
	if err != nil {
		return err
	}
	pipeline, repo, err := identity(runID)
	if err != nil {
		return err
	}
	if nodeID != tok.NodeID || pipeline != ownPipeline || ownRepo == 0 || repo != ownRepo {
		return ErrInputUndeclared
	}
	return nil
}

// ClaimAttemptOrdinal is the attempt ordinal tok's live claim will report,
// under which its node's durable log is written.
func (s *Store) ClaimAttemptOrdinal(ctx context.Context, tok ClaimToken) (int, error) {
	var consumed int
	err := s.queryRow(ctx, `SELECT attempts_consumed FROM nodes WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation).Scan(&consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrLockHeld
	}
	return consumed + 1, err
}
