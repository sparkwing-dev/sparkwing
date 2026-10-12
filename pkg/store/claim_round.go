package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// safety: a node made ready with a live runner that could take it waits this
// long before the dispatcher's own fallback may take it back.
const nodeClaimOfferWindow = 5 * time.Second

// ExecutorResource is a CPU and memory capacity or charge.
type ExecutorResource = match.Resources

// ClaimRoundResult reports whether the dispatcher may take a ready node back
// for its own fallback or must keep waiting for a runner's claim.
type ClaimRoundResult struct {
	Revoked bool
	Pending bool
}

type claimRoundSummary struct {
	HardCapabilities      []string         `json:"hard_capabilities,omitempty"`
	PreferredCapabilities []string         `json:"preferred_capabilities,omitempty"`
	Resources             ExecutorResource `json:"resources"`
	Slots                 int              `json:"slots"`
	RunPriority           int              `json:"run_priority"`
	RequiredCoordinatorID string           `json:"-"`
	RequiredLocation      string           `json:"-"`
}

type claimRoundEvent struct {
	claimRoundSummary
	Reason  string `json:"reason,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

func claimRoundSummaryTx(ctx context.Context, tx *storeTx, runID, nodeID string) (claimRoundSummary, error) {
	n := &nodeRecord{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID), n); err != nil {
		return claimRoundSummary{}, err
	}
	charge, err := nodeChargeTx(ctx, tx, runID, nodeID)
	if err != nil {
		return claimRoundSummary{}, err
	}
	var plan []byte
	if err := tx.QueryRowContext(ctx, `SELECT plan_json FROM runs WHERE id = ?`, runID).Scan(&plan); err != nil {
		return claimRoundSummary{}, err
	}
	return claimRoundSummary{
		HardCapabilities: append([]string(nil), n.NeedsLabels...), PreferredCapabilities: snapshotNodePrefers(plan, nodeID),
		Resources: charge, Slots: 1, RunPriority: snapshotRunPriority(plan),
		RequiredCoordinatorID: n.RequiredCoordinatorID, RequiredLocation: n.RequiredExecutorLocation,
	}, nil
}

// FinalizeClaimRound takes a ready node no runner claimed back from the queue
// once its round has run out, so the dispatcher's fallback may run it.
func (s *Store) FinalizeClaimRound(ctx context.Context, runID, nodeID string) (ClaimRoundResult, error) {
	return s.finalizeClaimRoundAt(ctx, runID, nodeID, time.Now())
}

func (s *Store) finalizeClaimRoundAt(ctx context.Context, runID, nodeID string, now time.Time) (ClaimRoundResult, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ClaimRoundResult{}, err
	}
	defer rollbackOrLog(tx)
	var readyAt, opened sql.NullInt64
	var claimedBy sql.NullString
	var status, requiredCoordinator, requiredLocation string
	err = tx.QueryRowContext(ctx, `SELECT ready_at, claimed_by, status, offer_started_at,
       required_coordinator_id, required_executor_location FROM nodes
	 WHERE run_id = ? AND node_id = ?`+tx.forUpdate(), runID, nodeID).Scan(
		&readyAt, &claimedBy, &status, &opened, &requiredCoordinator, &requiredLocation)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimRoundResult{}, ErrNotFound
	}
	if err != nil {
		return ClaimRoundResult{}, err
	}
	if !readyAt.Valid || claimedBy.Valid || status == nodeStatusDone {
		return ClaimRoundResult{}, tx.Commit()
	}
	if opened.Valid && now.Before(time.Unix(0, opened.Int64).Add(nodeClaimOfferWindow)) {
		return ClaimRoundResult{Pending: true}, tx.Commit()
	}
	if requiredCoordinator != "" || requiredLocation != "" {
		return ClaimRoundResult{Pending: true}, tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET ready_at = NULL, placement_hold_from = NULL, offer_started_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone, runID, nodeID)
	if err != nil {
		return ClaimRoundResult{}, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return ClaimRoundResult{}, err
	}
	if changed == 1 {
		summary, err := claimRoundSummaryTx(ctx, tx, runID, nodeID)
		if err != nil {
			return ClaimRoundResult{}, err
		}
		if _, err := appendRunEventTx(ctx, tx, runID, nodeID, "executor_offer_round_empty", claimRoundEvent{
			claimRoundSummary: summary, Reason: "no_live_offer", Outcome: "coordinator_fallback",
		}, now); err != nil {
			return ClaimRoundResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ClaimRoundResult{}, err
	}
	return ClaimRoundResult{Revoked: changed == 1}, nil
}

// safety: reports whether nothing but the coordinator could take the node, so
// its round may open already due.
// bug: a runner whose first poll lands after the controller's snapshot is missed.
func (s *Store) offerRoundIdleTx(ctx context.Context, tx *storeTx, runID string, summary claimRoundSummary,
	class int64, now time.Time,
) (bool, error) {
	live, known := ctx.Value(queueRunnersKey{}).([]RunnerPresence)
	if !known || summary.RequiredCoordinatorID != "" || summary.RequiredLocation != "" {
		return false, nil
	}
	team, _, err := runOwnerTx(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	warmCores, err := creditSettingTx(ctx, tx, metaKeyWarmCPUClass, DefaultWarmCPUClassCores)
	if err != nil {
		return false, err
	}
	metered, err := meteredRunnerTokensTx(ctx, tx, team, live)
	if err != nil {
		return false, err
	}
	var repo *match.Repository
	for _, runner := range live {
		isMetered, sameTeam := metered[runner.TokenPrefix]
		if !sameTeam {
			continue
		}
		demand := match.Demand{Selector: summary.HardCapabilities, Request: summary.Resources}
		if runner.Profile.Accept != nil {
			if repo == nil {
				read, err := scanRunRepository(tx.QueryRowContext(ctx, runRepositorySQL, runID, string(team)))
				if err != nil {
					return false, err
				}
				repo = &read
			}
			demand.Repo = repo
		}
		if !liveRunnerCould(runner, demand, false) {
			continue
		}
		// safety: mirrors warmClassFilter, which refuses a metered runner a class above the warm one.
		if isMetered && class != 0 && class > warmCores {
			continue
		}
		return false, nil
	}
	busy, err := liveGitHubCredentialForRunTx(ctx, tx, team, runID, now)
	return !busy, err
}

// safety: a GitHub Actions job claims only its own push's runs and may still be
// starting, so a live credential for the run's repository and commit keeps the
// window. The repository is compared exactly, outside SQL pattern matching.
func liveGitHubCredentialForRunTx(ctx context.Context, tx *storeTx, team Team, runID string, now time.Time) (_ bool, err error) {
	var owner, name string
	if err := tx.QueryRowContext(ctx, `SELECT github_owner, github_repo FROM triggers WHERE team = ? AND id = ?`,
		string(team), runID).Scan(&owner, &name); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.principal FROM github_runner_credentials c
  JOIN tokens t ON t.team = ? AND t.prefix = c.prefix
  JOIN triggers tr ON tr.team = ? AND tr.id = ?
 WHERE c.team = ? AND c.sha = tr.git_sha AND c.expires_at > ?
   AND (t.revoked_at IS NULL OR t.revoked_at > ?)`,
		string(team), string(team), runID, string(team), now.Unix(), now.Unix())
	if err != nil {
		return false, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var principal string
		if err := rows.Scan(&principal); err != nil {
			return false, err
		}
		parts := strings.SplitN(principal, ":", 3)
		if len(parts) == 3 && strings.EqualFold(parts[2], owner+"/"+name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// safety: maps each live runner credential of team to whether it is metered;
// another team's credential is absent, so its runner never holds the window.
func meteredRunnerTokensTx(ctx context.Context, tx *storeTx, team Team, live []RunnerPresence) (_ map[string]bool, err error) {
	metered := map[string]bool{}
	// safety: an unauthenticated runner is the local path, which belongs to DefaultTeam.
	if team == DefaultTeam {
		metered[""] = false
	}
	if len(live) == 0 {
		return metered, nil
	}
	args := []any{string(team)}
	for _, runner := range live {
		args = append(args, runner.TokenPrefix)
	}
	rows, err := tx.QueryContext(ctx, `SELECT prefix, metered FROM tokens WHERE team = ? AND prefix IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(live)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var prefix string
		var flag int64
		if err := rows.Scan(&prefix, &flag); err != nil {
			return nil, err
		}
		metered[prefix] = flag != 0
	}
	return metered, rows.Err()
}

// safety: the resolution order lives here rather than on the store so the
// credit ledger can price a node by the same cpu figure the scheduler sizes it
// by, inside the claim transaction that is already open.
func nodeChargeTx(ctx context.Context, q rowQuerier, runID, nodeID string) (ExecutorResource, error) {
	var team, pipeline string
	var plan []byte
	if err := q.QueryRowContext(ctx,
		`SELECT team, pipeline, plan_json FROM runs WHERE id = ?`, runID,
	).Scan(&team, &pipeline, &plan); err != nil {
		return ExecutorResource{}, err
	}
	if pin := snapshotNodeResource(plan, nodeID); pin.Cores > 0 || pin.MemoryBytes > 0 {
		return pin, nil
	}
	// safety: the team comes off the run rather than off the caller,
	// because this runs on the claim path, which is cross-team by
	// construction; without it a pipeline name two teams share prices
	// one team's node off the other's measurements.
	row := q.QueryRowContext(ctx, `
SELECT `+profileColumns+`
  FROM pipeline_profiles WHERE team = ? AND pipeline = ? AND node_id = ?`, team, pipeline, nodeID)
	profile, err := scanProfile(row, pipeline, nodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ExecutorResource{}, err
	}
	if err == nil {
		return nodeChargeFromSnapshot(plan, nodeID, profile), nil
	}
	return nodeChargeFromSnapshot(plan, nodeID, nil), nil
}

func nodeChargeFromSnapshot(plan []byte, nodeID string, profile *PipelineProfile) ExecutorResource {
	if pin := snapshotNodeResource(plan, nodeID); pin.Cores > 0 || pin.MemoryBytes > 0 {
		return pin
	}
	if profile != nil && (profile.PinnedCores > 0 || profile.PinnedMemoryBytes > 0) {
		return ExecutorResource{Cores: profile.PinnedCores, MemoryBytes: profile.PinnedMemoryBytes}
	}
	if profile != nil && profile.CPUMeasured {
		if profile.SampleCount >= 3 {
			cores := profile.SustainedCores
			if cores <= 0 {
				cores = profile.PeakCores
			}
			return ExecutorResource{Cores: math.Max(cores, 0.1), MemoryBytes: profile.PeakMemoryBytes}
		}
		cores := profile.PrevSustainedCores
		if cores <= 0 {
			cores = profile.PrevPeakCores
		}
		cores = math.Max(cores, 2*profile.FloorCores)
		// safety: overflow must not turn a measured memory floor into the default charge.
		floorMemory := int64(math.MaxInt64)
		if profile.FloorMemoryBytes <= math.MaxInt64/2 {
			floorMemory = 2 * profile.FloorMemoryBytes
		}
		memory := max(profile.PrevPeakMemoryBytes, floorMemory)
		if cores > 0 || memory > 0 {
			return ExecutorResource{Cores: cores, MemoryBytes: memory}
		}
	}
	return ExecutorResource{Cores: 1}
}

func snapshotNodeResource(raw []byte, nodeID string) ExecutorResource {
	var snapshot struct {
		Nodes []struct {
			ID        string `json:"id"`
			Modifiers *struct {
				Cores       float64 `json:"res_cores"`
				MemoryBytes int64   `json:"res_memory_bytes"`
			} `json:"modifiers"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return ExecutorResource{}
	}
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID && node.Modifiers != nil {
			return ExecutorResource{Cores: node.Modifiers.Cores, MemoryBytes: node.Modifiers.MemoryBytes}
		}
	}
	return ExecutorResource{}
}

func snapshotNodePrefers(raw []byte, nodeID string) []string {
	var snapshot struct {
		Nodes []struct {
			ID        string `json:"id"`
			Modifiers *struct {
				Prefers []string `json:"prefers"`
			} `json:"modifiers"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil
	}
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID && node.Modifiers != nil {
			return append([]string(nil), node.Modifiers.Prefers...)
		}
	}
	return nil
}

func snapshotRunPriority(raw []byte) int {
	var snapshot struct {
		Priority int `json:"priority"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return 0
	}
	return max(-100, min(100, snapshot.Priority))
}
