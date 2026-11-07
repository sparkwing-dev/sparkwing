package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// PlacementWait explains where one ready job with a repository tier list
// stands. The store computes it from committed state and its own clock, so the
// CLI and the console render the same answer.
type PlacementWait struct {
	JobID    string
	Revision string
	ReadyAt  time.Time
	Tiers    []PlacementTierWait
}

// PlacementTierWait is one tier's standing. OpensIn is zero once the tier can
// claim. Live names claimants that could take the job now; Busy counts
// matching claimants with no free slot; OnDemand means an idle launcher starts
// capacity when the tier opens. Unavailable says why the tier cannot take the
// job at all.
type PlacementTierWait struct {
	Tier        runspec.RepositoryTier
	OpensIn     time.Duration
	Live        []string
	Busy        int
	OnDemand    bool
	Unavailable string
}

type placementCandidate struct {
	name     string
	facts    claimFacts
	standing eligibility
	onDemand bool
}

func readPlacementWaitsTx(ctx context.Context, tx *storeTx, team Team, run RunRow) ([]PlacementWait, error) {
	policy := run.Request.Request.Placement
	if policy == nil || run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Dispatch != string(runspec.DispatchController) {
		return nil, nil
	}
	rows, err := tx.queryContext(ctx, `SELECT node_id,COALESCE(queue_started_at,ready_at) FROM nodes WHERE team=? AND run_id=? AND ready_at IS NOT NULL AND claimed_by IS NULL AND status='pending' AND cancel_requested_at IS NULL ORDER BY ready_at,node_id`, team, run.ID)
	if err != nil {
		return nil, err
	}
	var waits []PlacementWait
	for rows.Next() {
		var wait PlacementWait
		var ready int64
		if err := rows.Scan(&wait.JobID, &ready); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if len(policy.ForJob(wait.JobID)) > 0 {
			wait.Revision, wait.ReadyAt = policy.Revision, time.Unix(0, ready).UTC()
			waits = append(waits, wait)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(waits) == 0 {
		return nil, err
	}
	polls, err := placementRunnersTx(ctx, tx, team, time.Minute, true)
	if err != nil {
		return nil, err
	}
	observed := map[string]bool{}
	for _, poll := range polls {
		observed[poll.CapabilityPrefix] = true
	}
	launchers, err := unobservedLaunchersTx(ctx, tx, observed)
	if err != nil {
		return nil, err
	}
	ranking, err := claimRankingTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	now := tx.now(ctx)
	for i := range waits {
		unit := UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run.ID), NodeID: runspec.NodeID(waits[i].JobID)}
		var candidates []placementCandidate
		for _, poll := range polls {
			standing, err := explainUnitEligibilityTx(ctx, tx, team, unit, poll.UnitDemand)
			if err != nil {
				return nil, err
			}
			facts, err := claimFactsTx(ctx, tx, poll.UnitDemand)
			if refusal(err) || unavailableRunner(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, placementCandidate{name: facts.Name, facts: facts, standing: standing})
		}
		for _, prefix := range launchers {
			demand := UnitDemand{CapabilityPrefix: prefix}
			standing, err := explainUnitEligibilityTx(ctx, tx, team, unit, demand)
			if err != nil {
				return nil, err
			}
			facts, err := claimFactsTx(ctx, tx, demand)
			if refusal(err) || unavailableRunner(err) || err == nil && !standing.launcher {
				continue
			}
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, placementCandidate{name: facts.Name, facts: facts, standing: standing, onDemand: true})
		}
		waits[i].Tiers = placementTierWaits(policy.ForJob(waits[i].JobID), candidates, ranking, waits[i].ReadyAt, now)
	}
	return waits, nil
}

// safety: mirrors the claim query's hold; a claimant counts in its earliest matching tier only, a tier opens
// once every earlier tier with an admissible claimant has passed its cumulative deadline, and an on-demand
// launcher never holds.
func placementTierWaits(tiers []runspec.RepositoryTier, candidates []placementCandidate, ranking []string, ready, now time.Time) []PlacementTierWait {
	out := make([]PlacementTierWait, len(tiers))
	var deadline, blocked time.Time
	for i, tier := range tiers {
		out[i].Tier = tier
		if until := blocked.Sub(now); until > 0 {
			out[i].OpensIn = until
		}
		deadline = ready
		for _, earlier := range tiers[:i+1] {
			deadline = deadline.Add(earlier.Hold)
		}
		var funding, matched bool
		for _, c := range candidates {
			if slices.IndexFunc(tiers, func(t runspec.RepositoryTier) bool { return repositoryTierMatches(t, c.facts) }) != i {
				continue
			}
			matched = true
			switch {
			case c.standing.ok() && c.onDemand:
				out[i].OnDemand = true
			case c.standing.ok() && c.facts.FreeSlots > 0:
				out[i].Live = append(out[i].Live, c.name)
			case c.standing.ok():
				out[i].Busy++
			case c.standing.failed == stepBilling:
				funding = true
			}
		}
		if len(out[i].Live) > 0 && deadline.After(blocked) {
			blocked = deadline
		}
		switch {
		case len(ranking) > 0 && !slices.Contains(ranking, rankingTier(tier.Target)):
			out[i].Unavailable = "off in this team's runner ranking"
		case len(out[i].Live) > 0 || out[i].Busy > 0 || out[i].OnDemand:
		case funding:
			out[i].Unavailable = "Cloud credits cannot fund it"
		case tier.Target == runspec.PlacementGitHubActions:
			out[i].Unavailable = "no live GitHub Actions runner; on-demand workflow dispatch is not available yet"
		case matched:
			out[i].Unavailable = "no matching runner can take this job"
		default:
			out[i].Unavailable = "no live runner"
		}
	}
	return out
}

func repositoryTierMatches(tier runspec.RepositoryTier, f claimFacts) bool {
	switch tier.Target {
	case runspec.PlacementCloud:
		return f.Cloud == 1
	case runspec.PlacementGitHubActions:
		return f.Tier == tierActions
	}
	if f.Tier != tierSelfHosted {
		return false
	}
	if name, ok := strings.CutPrefix(tier.Target, "name="); ok && f.Name != name {
		return false
	}
	for _, term := range tier.Labels {
		if !match.TermSatisfied(match.Profile{Labels: f.Labels}, term) {
			return false
		}
	}
	return true
}

func rankingTier(target string) string {
	switch target {
	case runspec.PlacementCloud:
		return TierCloud
	case runspec.PlacementGitHubActions:
		return tierActions
	}
	return tierSelfHosted
}
