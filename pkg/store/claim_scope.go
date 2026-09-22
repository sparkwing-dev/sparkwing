package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrClaimantHasNoTeam means a claim arrived on a credential no token row
// backs, so the team it may claim for cannot be established.
var ErrClaimantHasNoTeam = errors.New("store: claim credential carries no team")

// safety: the team a machine may take work for comes off its credential and
// never off the request body, because the request is the caller's to write.
// A claim that cannot name its team takes nothing rather than falling back to
// a team it might not own; soleTeamScope says when that arm is reachable.
func (s *Store) claimScope(ctx context.Context, claimant ClaimIdentity) (teamScope, error) {
	// safety: an unbound claim is the unauthenticated local path, whose rows
	// all carry the team the tenant migration put them in.
	if !claimant.bound() {
		return oneTeam(DefaultTeam), nil
	}
	var team string
	var metered int64
	err := s.queryRow(ctx,
		`SELECT team, metered FROM tokens WHERE prefix = ?`, claimant.TokenPrefix).Scan(&team, &metered)
	if errors.Is(err, sql.ErrNoRows) {
		return s.soleTeamScope(ctx)
	}
	if err != nil {
		return teamScope{}, err
	}
	// safety: a metered token is the operator's own pool, which is what
	// [Store.ClaimNamedNode] already holds the cpu-class ladder against. That
	// pool is the overflow every team's queue drains onto, so it is the one
	// claimant that reads across teams; every other credential is one team's.
	if metered != 0 {
		return allTeams(), nil
	}
	scoped := NormalizeTeam(Team(team))
	if scoped == "" {
		return teamScope{}, ErrClaimantHasNoTeam
	}
	return oneTeam(scoped), nil
}

// safety: a prefix no token row backs is a credential nothing can place. One
// registered team is the single-tenant install decision 0004 leaves unchanged,
// and there is nowhere else the claim could belong. A second team makes the
// same credential claim nothing, because guessing which it meant is the leak.
func (s *Store) soleTeamScope(ctx context.Context) (teamScope, error) {
	teams, err := s.AsOperator().ListTeams(ctx)
	if err != nil {
		return teamScope{}, err
	}
	if len(teams) == 1 {
		return oneTeam(teams[0]), nil
	}
	return teamScope{}, ErrClaimantHasNoTeam
}

// safety: refuses a node of another team before any scheduling state is
// written, so a caller that names a node it was never offered is turned away
// with the same not-found a missing node returns and learns nothing about
// whether the node exists.
func (s *Store) assertClaimantOwnsNode(ctx context.Context, claimant ClaimIdentity, runID, nodeID string) error {
	scope, err := s.claimScope(ctx, claimant)
	if err != nil {
		return err
	}
	if scope.all {
		return nil
	}
	var found string
	err = s.queryRow(ctx,
		`SELECT node_id FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(scope.team), runID, nodeID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("node", runID+"/"+nodeID)
	}
	return err
}

// safety: refuses a scope carrying neither a team nor the all-teams flag,
// because the zero value would otherwise render as an empty predicate and
// hand one claimant the whole deployment's queue.
func claimTeamWhere(scope teamScope, column string) (string, []any, error) {
	if scope.all {
		return "", nil, nil
	}
	if scope.team == "" {
		return "", nil, ErrClaimantHasNoTeam
	}
	return " AND " + column + " = ?", []any{string(scope.team)}, nil
}
