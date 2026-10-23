package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
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
