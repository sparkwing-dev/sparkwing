package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func placementRank(profile match.Profile, demand match.Demand, preferences []PlacementPreference) int {
	if !match.Evaluate(profile, demand).OK() {
		return len(preferences)
	}
	profile.Labels = match.AuthenticatedLabels(profile)
	if profile.Class == match.ClassCloud {
		size, err := match.CloudSizeForProfile(demand.Selector, profile)
		if err != nil {
			return len(preferences)
		}
		profile.Labels = placementSizeLabels(profile.Labels, size)
	}
	for i, preference := range preferences {
		if len(preference.Prefers) == 0 || slices.ContainsFunc(preference.Prefers, func(term string) bool {
			return strings.TrimSpace(term) != "" && match.TermSatisfied(profile, term)
		}) {
			return i
		}
	}
	return len(preferences)
}

func placementSizeLabels(labels []string, size match.CloudSize) []string {
	labels = slices.DeleteFunc(slices.Clone(labels), func(label string) bool { return strings.HasPrefix(label, "sparkwing-") })
	return append(labels, size.Label)
}

func placementRunnersTx(ctx context.Context, tx *storeTx, team Team, liveness time.Duration, launchers bool) ([]runnerPoll, error) {
	if liveness <= 0 {
		return nil, nil
	}
	now := tx.now(ctx)
	cutoff := now.Add(-liveness)
	query := `SELECT prefix,claim_observation_json FROM capabilities WHERE team=? AND last_used_at>=? AND claim_observation_json IS NOT NULL`
	args := []any{team}
	if launchers {
		query = `SELECT prefix,claim_observation_json FROM capabilities WHERE COALESCE(team,'') IN (?,?) AND (team IS NOT NULL OR subject_kind='service') AND last_used_at>=? AND claim_observation_json IS NOT NULL`
		args = append(args, "")
	}
	args = append(args, cutoff.UnixNano(), limits.RankingObservations)
	rows, err := tx.queryContext(ctx, query+` ORDER BY last_used_at DESC,prefix LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var live []runnerPoll
	for rows.Next() {
		var prefix string
		var body []byte
		var observation runnerPoll
		if err := rows.Scan(&prefix, &body); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if err := json.Unmarshal(body, &observation); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if observation.ObservedAt.Before(cutoff) || observation.ObservedAt.After(now) {
			continue
		}
		observation.CapabilityPrefix = prefix
		live = append(live, observation)
	}
	return live, errors.Join(rows.Err(), rows.Close())
}
