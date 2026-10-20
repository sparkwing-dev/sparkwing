package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// Team is a tenant key. It is a defined type rather than a bare string
// because every scoped statement takes one, and a string parameter
// would let a run id or a pipeline name reach the tenant column
// unnoticed.
type Team string

// DefaultTeam owns the standalone local controller's runs.
const DefaultTeam Team = "default"

// ErrNoTeam is what ForTeam returns for an empty team, because a query
// scoped to the empty team matches no row at all and a caller that lost
// its team would read that as an empty install rather than as a bug.
var ErrNoTeam = errors.New("store: team is required")

// Tenant confines reads and writes to one registered team.
type Tenant struct {
	s    *Store
	team Team
}

// ErrUnknownTeam reports a handle asked for a team that is not
// registered.
var ErrUnknownTeam = errors.New("store: team is not registered")

// NormalizeTeam is the one spelling of a team name. Decision 0004 routes
// on `<team>.sparkwing.dev` and DNS is case-insensitive, so Acme and acme
// address one tenant and must not become two.
func NormalizeTeam(team Team) Team {
	return Team(strings.ToLower(strings.TrimSpace(string(team))))
}

// ForTeam returns a tenant handle for a registered team without a pending deletion.
func (s *Store) ForTeam(ctx context.Context, team Team) (*Tenant, error) {
	return s.forTeam(ctx, team)
}

func (s *Store) forTeam(ctx context.Context, team Team) (*Tenant, error) {
	team = NormalizeTeam(team)
	if team == "" {
		return nil, ErrNoTeam
	}
	var name string
	err := s.queryRow(ctx, liveTeamsSQL+` AND name = ?`, string(team)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTeam, team)
	}
	if err != nil {
		return nil, err
	}
	return &Tenant{s: s, team: team}, nil
}

// Team reports which team t is scoped to.
func (t *Tenant) Team() Team { return t.team }

// Operator is the unscoped view of the store. It does not embed *Store,
// because an embedded *Store would hand every method on the store to
// anything holding an operator and the unscoped surface would grow by
// default instead of by review. Each unscoped operation is a named
// method here, added when a maintenance path needs it.
//
// The fleet-wide readers carry AcrossTeams in the name, because an
// empty-string team standing for "every team" reads as a missing team to
// everyone who sees it and turns a Tenant that lost its scope into a
// fleet reader.
type Operator struct{ s *Store }

// AsOperator returns the unscoped handle.
func (s *Store) AsOperator() *Operator { return &Operator{s: s} }

// RunTeam reports which team owns the run runID, for a sweep that found the
// id across teams and has to act on it through that team's handle.
// [ErrNotFound] when no run carries the id.
func (o *Operator) RunTeam(ctx context.Context, runID string) (Team, error) {
	return o.s.runTeam(ctx, runID)
}

func (s *Store) runTeam(ctx context.Context, runID string) (Team, error) {
	var team string
	err := s.queryRow(ctx, `SELECT team FROM runs WHERE id = ?`, runID).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("run", runID)
	}
	return Team(team), err
}

// safety: a type rather than an empty team meaning "all", because that
// sentinel cannot be told from a team a caller failed to set; the zero
// value here scopes to the empty team, which matches nothing.
type teamScope struct {
	team Team
	all  bool
}

func oneTeam(team Team) teamScope { return teamScope{team: team} }

func allTeams() teamScope { return teamScope{all: true} }

var (
	tenantTables   = schemaTableNames(SchemaTenant)
	operatorTables = schemaTableNames(SchemaOperator)
)

func schemaTableNames(owner SchemaOwner) []string {
	var names []string
	for _, table := range SchemaTables {
		if table.Owner == owner {
			names = append(names, table.Name)
		}
	}
	return names
}

// safety: a local install starts with an explicit registered team and its authority generation.
func recordDefaultTeamTx(ctx context.Context, tx *storeTx) error {
	return registerTeamTx(ctx, tx, DefaultTeam)
}

func registerTeamTx(ctx context.Context, tx *storeTx, team Team) error {
	now := tx.now().UnixNano()
	_, err := tx.ExecContext(ctx,
		`INSERT INTO teams (name, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO NOTHING`,
		string(team), now, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, team)
	return err
}

// liveTeamsSQL is shared by handle acquisition and fleet maintenance.
const liveTeamsSQL = `SELECT name FROM teams WHERE NOT EXISTS (SELECT 1 FROM team_deletions d WHERE d.slug = teams.name AND d.state = 'pending')`

// EachTeam visits selected live tenants, or all live tenants when query is nil.
// A tenant's failure is logged and cannot prevent another tenant's maintenance.
func (o *Operator) EachTeam(ctx context.Context, query []Team, fn func(*Tenant) error) error {
	rows, err := o.s.query(ctx, liveTeamsSQL+` ORDER BY name`)
	if err != nil {
		return err
	}
	var teams []Team
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team); err != nil {
			return errors.Join(err, rows.Close())
		}
		if query == nil || slices.Contains(query, team) {
			teams = append(teams, team)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, team := range teams {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(&Tenant{s: o.s, team: team}); err != nil {
			slog.ErrorContext(ctx, "tenant maintenance failed", "team", team, "err", err)
		}
	}
	return ctx.Err()
}
