package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
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
	if r.Options.Runner.Set && r.Options.Runner.Value.Kind != runspec.RunnerAny {
		return false
	}
	return !slices.ContainsFunc(r.Options.Requires.Value, func(term string) bool {
		return requiresPool(term) || slices.Contains(strings.Split(strings.ReplaceAll(term, " ", ""), ","), "class="+match.ClassCloud)
	})
}

func automatedRun(r runspec.Request) bool {
	return r.Origin == runspec.OriginApp || r.Origin == runspec.OriginSchedule
}

func sealedRequestTx(ctx context.Context, tx *storeTx, team Team, run string) (runspec.SealedRequest, error) {
	var body []byte
	var seal runspec.SealedRequest
	if err := tx.QueryRowContext(ctx, `SELECT request_json FROM runs WHERE team=? AND id=?`, team, run).Scan(&body); err != nil {
		return seal, err
	}
	return seal, json.Unmarshal(body, &seal)
}

func claimTierTx(ctx context.Context, tx *storeTx, chain []authorityNode, run string) (string, error) {
	if launcherClaimant(chain[0].principal.Capability) {
		return TierCloud, nil
	}
	for _, ancestor := range chain {
		c := ancestor.principal.Capability
		if c.Purpose == capability.PurposeHere && c.Binding.RunID == run {
			return TierHere, nil
		}
		if c.Binding.Kind == capability.BindingPush {
			return TierActions, nil
		}
	}
	root := chain[len(chain)-1].principal.Capability
	if root.Subject.Kind != capability.SubjectMachine || root.Parent != nil {
		return "", nil
	}
	record, err := loadCapabilityRecordTx(ctx, tx, root.Prefix)
	if err != nil || record.Origin == nil || !record.Origin.Personal {
		return TierSelfHosted, err
	}
	return TierPersonal, nil
}

func claimRankTx(ctx context.Context, tx *storeTx, team Team, chain []authorityNode, run string) (int, error) {
	ranking, err := claimRankingTx(ctx, tx, team)
	if err != nil || len(ranking) == 0 {
		return -1, err
	}
	tier, err := claimTierTx(ctx, tx, chain, run)
	if err != nil {
		return -1, err
	}
	rank := slices.Index(ranking, tier)
	if rank < 0 {
		return -1, authorityError(apierr.Forbidden, "claimant tier is outside the team's claim ranking")
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

// safety: a polled better-ranked claimant keeps priority for one more window, so a dead winner cannot starve the unit.
func rankingHoldTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, claimant string, liveness time.Duration) (bool, error) {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claimant}, false)
	if err != nil {
		return false, err
	}
	rank, err := claimRankTx(ctx, tx, team, chain, unit.RunID)
	if err != nil || rank <= 0 {
		return false, err
	}
	since, ready, err := unitReadyTx(ctx, tx, team, unit)
	if err != nil || !ready {
		return false, err
	}
	now, end := tx.now(), since.Add(limits.RankingWindow)
	if !now.Before(end.Add(limits.RankingWindow)) {
		return false, nil
	}
	polls, err := placementRunnersTx(ctx, tx, team, liveness, true)
	if err != nil {
		return false, err
	}
	for _, poll := range polls {
		if poll.CapabilityPrefix == claimant || len(poll.Kinds) > 0 && !slices.Contains(poll.Kinds, unit.Kind) {
			continue
		}
		if !now.Before(end) && (poll.ObservedAt.Before(since) || !poll.ObservedAt.Before(end)) {
			continue
		}
		better, err := betterRankedTx(ctx, tx, team, unit, poll, rank)
		if err != nil || better {
			return better, err
		}
	}
	return false, nil
}

func betterRankedTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, poll runnerPoll, rank int) (bool, error) {
	profile, required, err := placementRunnerTx(ctx, tx, team, unit, poll.UnitDemand)
	var denied *apierr.Error
	if errors.As(err, &denied) && (denied.Code == apierr.Unauthenticated || denied.Code == apierr.Forbidden) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !match.Evaluate(profile, required).OK() || !match.Fits(profile.Capacity, poll.Available, required.Request).OK() {
		return false, nil
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: poll.CapabilityPrefix}, false)
	if err != nil {
		return false, err
	}
	theirs, err := claimRankTx(ctx, tx, team, chain, unit.RunID)
	if err != nil || theirs >= rank {
		return false, err
	}
	if !launcherClaimant(chain[0].principal.Capability) {
		return true, nil
	}
	price, err := unitBillingPriceTx(ctx, tx, required.Request)
	var unpriced *UnpricedCPUClassError
	if errors.As(err, &unpriced) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return eligibleUnitBillingTx(ctx, tx, team, unit, price, true)
}
