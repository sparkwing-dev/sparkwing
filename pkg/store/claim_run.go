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

type declaredPlanNode struct {
	ID           string                 `json:"id"`
	Modifiers    declaredClaimModifiers `json:"modifiers"`
	PipelineRefs []PlanPipelineRef      `json:"pipeline_refs"`
}

type declaredClaimModifiers struct {
	Cache        bool   `json:"cache"`
	ConcGroup    string `json:"conc_group"`
	ConcCapacity int    `json:"conc_capacity"`
	ConcCost     int    `json:"conc_cost"`
	ConcScope    string `json:"conc_scope"`
	ConcOnLimit  string `json:"conc_on_limit"`
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
	return s.declaredGeneratedNode(ctx, team, runID, nodeID)
}

// safety: a generated node's spec is the run host's record of what its generator returned, admitted beside the
// plan; it carries no secrets of its own, so the pipeline's declared secrets still bound what it reads.
func (s *Store) declaredGeneratedNode(ctx context.Context, team Team, runID, nodeID string) (declaredPlanNode, bool, error) {
	var raw []byte
	var specHash string
	err := s.queryRow(ctx, `SELECT generated_json, spec_hash FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID).Scan(&raw, &specHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(raw) == 0) {
		return declaredPlanNode{}, false, nil
	}
	if err != nil {
		return declaredPlanNode{}, false, err
	}
	var gen GeneratedNode
	var n declaredPlanNode
	if err := json.Unmarshal(raw, &gen); err != nil {
		return declaredPlanNode{}, false, fmt.Errorf("read run %s node %s's generated spec: %w", runID, nodeID, err)
	}
	if err := json.Unmarshal(gen.Spec, &n); err != nil {
		return declaredPlanNode{}, false, fmt.Errorf("read run %s node %s's generated spec: %w", runID, nodeID, err)
	}
	var stamp struct {
		SpecHash string `json:"spec_hash"`
	}
	// safety: a spec that names another node or hashes otherwise than its row is not what the run accepted.
	if err := json.Unmarshal(gen.Spec, &stamp); err != nil {
		return declaredPlanNode{}, false, fmt.Errorf("read run %s node %s's generated spec: %w", runID, nodeID, err)
	}
	if n.ID != nodeID || stamp.SpecHash != specHash {
		return declaredPlanNode{}, false, nil
	}
	return n, true, nil
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
	// ArtifactManifest is a memoized source's artifact manifest digest, which
	// a hit or coalesce follower records as its own; empty for last_run.
	ArtifactManifest string `json:"artifact_manifest,omitempty"`
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
		// safety: the hash is the claim's own; any entry it can name was written
		// by this same repository's pipeline node, whose code sets the key anyway.
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
		if err := s.sameMemoIdentity(ctx, tok, req.Kind, runID, nodeID); err != nil {
			return ClaimInput{}, err
		}
	}
	in := ClaimInput{RunID: runID, NodeID: nodeID}
	var status, outcome, manifest string
	var finished sql.NullInt64
	err = s.queryRow(ctx, `SELECT status, outcome, finished_at, artifact_manifest FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`, string(tok.Team), runID, nodeID).Scan(&status, &outcome, &finished, &manifest)
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
	// safety: only a memoized source, already bound to this claim's team, repository, pipeline and node, hands
	// on its manifest; a pipeline reference reads another pipeline's run, whose artifacts the node never declared.
	if req.Kind != ClaimInputLastRun {
		in.ArtifactManifest = manifest
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

// safety: a memoized result reaches a claim only from its own team,
// repository, pipeline and node, whoever wrote the entry under its key, so a
// hash known from elsewhere in the team discloses nothing.
func (s *Store) sameMemoIdentity(ctx context.Context, tok ClaimToken, kind ClaimInputKind, runID, nodeID string) error {
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
	if nodeID != tok.NodeID || pipeline != ownPipeline || ownRepo == 0 {
		return ErrInputUndeclared
	}
	// safety: a cache entry another repository wrote under a name this one now
	// uses reads as a miss, so the node runs rather than fail; a coalesce
	// leader's is refused, as a missing leader output would pass for success.
	if repo != ownRepo {
		if kind == ClaimInputCached {
			return notFound("cache entry of this repository", runID+"/"+nodeID)
		}
		return ErrInputUndeclared
	}
	return nil
}
