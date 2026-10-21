package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Claim tiers order run-anywhere claimants in a team's claim ranking.
const (
	TierHere       = "here"
	TierPersonal   = "personal"
	TierActions    = "github-actions"
	TierSelfHosted = "self-hosted"
	TierCloud      = "cloud"
)

// ClaimRankingDefault, as a team's whole ranking update, restores the default ranking.
const ClaimRankingDefault = "default"

// DefaultClaimRanking is the ranking of a team that has not set one.
var DefaultClaimRanking = []string{TierHere, TierPersonal, TierActions, TierSelfHosted, TierCloud}

// ValidateClaimRanking refuses unknown and repeated tiers; an empty ranking means first come.
func ValidateClaimRanking(tiers []string) error {
	for i, tier := range tiers {
		if !slices.Contains(DefaultClaimRanking, tier) || slices.Contains(tiers[:i], tier) {
			return authorityError(apierr.BadRequest, "claim ranking takes distinct tiers from "+strings.Join(DefaultClaimRanking, ", "))
		}
	}
	return nil
}

func claimRankingTx(ctx context.Context, tx *storeTx, team Team) ([]string, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT claim_ranking FROM teams WHERE name=?`, team).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return DefaultClaimRanking, nil
	}
	var tiers []string
	if err := json.Unmarshal([]byte(raw), &tiers); err != nil {
		return nil, err
	}
	return tiers, ValidateClaimRanking(tiers)
}

func runAnywhere(r runspec.Request) bool {
	if r.Mode != runspec.IntakeExecute || r.Dispatch != runspec.DispatchController || r.Burst != nil {
		return false
	}
	return !(r.Options.Runner.Set && r.Options.Runner.Value.Kind != runspec.RunnerAny) && !slices.ContainsFunc(r.Options.Requires.Value, pinnedTerm)
}

// safety: a unit whose own requirements pin a runner, pool or class skips ranking so a dropped tier cannot strand it.
func rankedUnit(r runspec.Request, demand match.Demand) bool {
	return runAnywhere(r) && !slices.ContainsFunc(demand.Selector, pinnedTerm)
}

func pinnedTerm(term string) bool {
	for _, alternative := range strings.Split(term, ",") {
		alternative = strings.TrimSpace(alternative)
		if strings.HasPrefix(alternative, "pool=") || strings.HasPrefix(alternative, "name=") || strings.HasPrefix(alternative, "class=") {
			return true
		}
	}
	return false
}

func sealedRequestTx(ctx context.Context, tx *storeTx, team Team, run string) (runspec.SealedRequest, error) {
	var body []byte
	var seal runspec.SealedRequest
	if err := tx.QueryRowContext(ctx, `SELECT request_json FROM runs WHERE team=? AND id=?`, team, run).Scan(&body); err != nil {
		return seal, err
	}
	return seal, json.Unmarshal(body, &seal)
}

var errTierOutsideRanking = authorityError(apierr.Forbidden, "claimant tier is outside the team's claim ranking")

func claimRankTx(ctx context.Context, tx *storeTx, team Team, chain []authorityNode, request runspec.Request) (int, error) {
	ranking, err := claimRankingTx(ctx, tx, team)
	if err != nil || len(ranking) == 0 {
		return -1, err
	}
	facts, err := claimantFactsTx(ctx, tx, chain, request)
	if err != nil {
		return -1, err
	}
	rank := slices.Index(ranking, facts.Tier)
	if rank < 0 {
		return -1, errTierOutsideRanking
	}
	return rank, nil
}

func unitReadyTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) (time.Time, bool, error) {
	var at sql.NullInt64
	var err error
	switch unit.Kind {
	case capability.UnitNode:
		err = tx.QueryRowContext(ctx, `SELECT ready_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&at)
	case capability.UnitEval:
		err = tx.QueryRowContext(ctx, `SELECT finished_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&at)
	case capability.UnitPlan:
		err = tx.QueryRowContext(ctx, `SELECT available_at FROM triggers WHERE team=? AND id=? AND unit_kind='plan'`, team, unit.RunID).Scan(&at)
	}
	return time.Unix(0, at.Int64).UTC(), at.Valid, err
}

// safety: every claim decision for a unit, an ask or an award, holds the unit's ranking lock from before it reads
// readiness until its transaction ends, so asks and awards serialize. It is always taken before the unit's rows,
// and scans take it in one global unit order, so claims cannot deadlock.
func lockRankedUnitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	return advisoryLockTx(ctx, tx, "ranking:"+string(team)+"/"+string(unit.RunID)+"/"+string(unit.Kind)+"/"+string(unit.NodeID)+"/"+strconv.FormatInt(unit.SourceGeneration, 10))
}

// safety: a held claimant's ask is written in the claim transaction, whether that transaction awards another unit
// or nothing, so later polls cannot erase it. For RankingAward after the window, only the window's winner, the
// best-ranked asker recorded inside it, may take the unit; a newcomer of any rank waits for the deadline.
func rankingHoldTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, request runspec.Request, claimant string, liveness time.Duration) (bool, error) {
	if err := lockRankedUnitTx(ctx, tx, team, unit); err != nil {
		return false, err
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claimant}, false)
	if err != nil {
		return false, err
	}
	rank, err := claimRankTx(ctx, tx, team, chain, request)
	if err != nil || rank < 0 {
		return false, err
	}
	since, ready, err := unitReadyTx(ctx, tx, team, unit)
	if err != nil || !ready {
		return false, err
	}
	now, end := tx.now(ctx), since.Add(limits.RankingWindow)
	if !now.Before(end) {
		if !now.Before(end.Add(limits.RankingAward)) {
			return false, nil
		}
		var winner string
		err := tx.QueryRowContext(ctx, `SELECT claimant FROM unit_asks WHERE team=? AND run_id=? AND unit_kind=? AND node_id=? AND asked_at>=? AND asked_at<? ORDER BY claim_rank,asked_at,claimant LIMIT 1`, team, unit.RunID, unit.Kind, unit.NodeID, since.UnixNano(), end.UnixNano()).Scan(&winner)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return winner != claimant, err
	}
	if rank == 0 {
		return false, nil
	}
	polls, err := placementRunnersTx(ctx, tx, team, liveness, true)
	if err != nil {
		return false, err
	}
	for _, poll := range polls {
		if poll.CapabilityPrefix == claimant {
			continue
		}
		theirs, err := unitEligibilityTx(ctx, tx, team, unit, poll.UnitDemand, eligibilityChecks{fit: true, billing: true})
		if err != nil {
			return false, err
		}
		if !theirs.ok() {
			continue
		}
		better, err := claimRankTx(ctx, tx, team, theirs.chain, request)
		if err != nil || better < 0 || better >= rank {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO unit_asks(team,run_id,unit_kind,node_id,claimant,claim_rank,asked_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(team,run_id,unit_kind,node_id,claimant) DO UPDATE SET claim_rank=excluded.claim_rank,asked_at=excluded.asked_at WHERE unit_asks.asked_at<?`, team, unit.RunID, unit.Kind, unit.NodeID, claimant, rank, now.UnixNano(), since.UnixNano())
		return true, err
	}
	return false, nil
}
