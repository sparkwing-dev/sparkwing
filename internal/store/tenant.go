package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	sqlite3 "modernc.org/sqlite/lib"
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

// ForTeam returns a tenant handle for a registered team that has never begun deletion.
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
	var team string
	err := o.s.queryRow(ctx, `SELECT team FROM runs WHERE id = ?`, runID).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("run", runID)
	}
	return Team(team), err
}

var (
	tenantTables   = schemaTableNames(schemaTenant)
	operatorTables = schemaTableNames(schemaOperator)
)

func schemaTableNames(owner schemaOwner) []string {
	var names []string
	for _, table := range schemaTables {
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
	now := tx.now(ctx).UnixNano()
	_, err := tx.execContext(ctx,
		`INSERT INTO teams (name, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO NOTHING`,
		string(team), now, now)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, team)
	return err
}

// liveTeamsSQL is shared by handle acquisition and fleet maintenance.
const liveTeamsSQL = `SELECT name FROM teams WHERE NOT EXISTS (SELECT 1 FROM team_deletions d WHERE d.slug = teams.name)`

// EachTeam visits selected live tenants, or all live tenants when query is nil.
// Tenant failures are logged; database failures and loss of the maintenance lease stop the sweep.
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
			var sqlite interface{ Code() int }
			var postgres interface{ SQLState() string }
			global := errors.Is(err, ErrLockHeld) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone)
			if errors.As(err, &sqlite) {
				code := sqlite.Code() & 0xff
				global = global || code != sqlite3.SQLITE_CONSTRAINT && code != sqlite3.SQLITE_MISMATCH
			}
			if errors.As(err, &postgres) {
				state := postgres.SQLState()
				global = global || !strings.HasPrefix(state, "22") && !strings.HasPrefix(state, "23")
			}
			if global {
				return err
			}
			if dbErr := o.Ping(ctx); dbErr != nil {
				return errors.Join(err, dbErr)
			}
			slog.ErrorContext(ctx, "tenant maintenance failed", "team", team, "err", err)
		}
	}
	return ctx.Err()
}
