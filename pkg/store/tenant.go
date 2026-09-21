package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Team is a tenant key. It is a defined type rather than a bare string
// because every scoped statement takes one, and a string parameter
// would let a run id or a pipeline name reach the tenant column
// unnoticed.
type Team string

// DefaultTeam owns every row that existed before the team column did.
// A single-tenant install migrates into it and keeps behaving as one
// team without being configured, because the column's default is this
// value and a statement that names no team writes it anyway.
const DefaultTeam Team = "default"

// ErrNoTeam is what ForTeam returns for an empty team, because a query
// scoped to the empty team matches no row at all and a caller that lost
// its team would read that as an empty install rather than as a bug.
var ErrNoTeam = errors.New("store: team is required")

// Tenant is the team-scoped view of the store. Its methods take no team
// argument and every statement they issue carries `team = ?`, so a
// caller holding a Tenant can reach neither another team's rows nor
// every team's rows. The *Store it wraps is an unexported field, so no
// package outside this one can widen a Tenant back to the unscoped
// surface.
//
// Moving a method family onto Tenant is three mechanical edits: take
// the receiver from *Store to *Tenant, add `team = ?` to every WHERE
// and `team` to every INSERT column list, and pass t.team as the
// argument. tenant_runs.go is the worked example.
type Tenant struct {
	s    *Store
	team Team
}

// ForTeam returns the handle through which tenant-owned rows are read
// and written.
func (s *Store) ForTeam(team Team) (*Tenant, error) {
	if strings.TrimSpace(string(team)) == "" {
		return nil, ErrNoTeam
	}
	return &Tenant{s: s, team: team}, nil
}

// Team reports which team t is scoped to.
func (t *Tenant) Team() Team { return t.team }

// Operator is the unscoped view of the store: every method on it reads
// and writes across all teams. Reaping, maintenance and migration paths
// take it by name, so a statement that crosses teams is visible in the
// code that asks for one. It embeds *Store because a method that has
// not yet moved to Tenant is unscoped already, which is what an
// operator wants; each family the port moves leaves Operator holding
// only what a maintenance path still needs.
type Operator struct{ *Store }

// AsOperator returns the unscoped handle.
func (s *Store) AsOperator() *Operator { return &Operator{Store: s} }

const teamsTableSQLite = `CREATE TABLE IF NOT EXISTS teams (
    name       TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);`

var teamsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(teamsTableSQLite)

// safety: the default is what carries an install that predates the column,
// because a statement written before the port names no team at all and its
// rows have to land in the team the migration put the existing rows in.
var teamColumn = map[string]string{
	"team": "TEXT NOT NULL DEFAULT '" + string(DefaultTeam) + "'",
}

// safety: a table is on this list or on operatorTables and never on both,
// because a scoped query on an unclassified table cannot be written; the
// guard in tenant_classification_internal_test.go refuses either mistake.
var tenantTables = []string{
	"agent_loss_retries",
	"agent_loss_retry_legacy_deny_all",
	"agent_loss_retry_node_sources",
	"approvals",
	"concurrency_cache",
	"concurrency_entries",
	"concurrency_holders",
	"concurrency_waiters",
	"credit_charges",
	"credit_grants",
	"cron_fires",
	"cron_schedules",
	"debug_pauses",
	"egress_usage",
	"events",
	"github_webhook_bindings",
	"node_bounces",
	"node_claim_offers",
	"node_dispatches",
	"node_execution_attempts",
	"node_metrics",
	"node_steps",
	"nodes",
	"pipeline_profiles",
	"run_definition_plans",
	"runs",
	"secrets",
	"sessions",
	"storage_month_usage",
	"storage_quotas",
	"storage_run_usage",
	"tokens",
	"triggers",
	"users",
}

// safety: executors is here because an executor enrolls with the deployment
// and is offered work from every team on it, and sparkwing_meta because the
// bag is the deployment's; the per-team keys inside that bag need a table of
// their own rather than a column on it.
var operatorTables = []string{
	"executors",
	"sparkwing_meta",
	"sparkwing_requirements",
	"sparkwing_schema_version",
	"teams",
}

// safety: a tenant listing orders by started_at within one team, and
// the index it would otherwise use leads with started_at across every
// team, so without this one team's listing scans the fleet's runs.
const runsTeamIndex = `CREATE INDEX IF NOT EXISTS idx_runs_team_started ON runs(team, started_at DESC);`

func applyTenantKeyMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if _, err := tx.ExecContext(ctx, teamsTableSQLite); err != nil {
		return err
	}
	for _, table := range tenantTables {
		if err := ensureColumnsSQLite(ctx, tx, table, teamColumn); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, runsTeamIndex); err != nil {
		return err
	}
	return recordDefaultTeamTx(ctx, tx)
}

func applyTenantKeyMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if _, err := tx.ExecContext(ctx, teamsTablePostgres); err != nil {
		return err
	}
	for _, table := range tenantTables {
		if err := addColumnsTx(ctx, tx, table, teamColumn); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, runsTeamIndex); err != nil {
		return err
	}
	return recordDefaultTeamTx(ctx, tx)
}

// safety: the column's default names a team, so it is registered here too,
// because a migrated single-tenant install listing no team would read as an
// install whose rows belong to nobody.
func recordDefaultTeamTx(ctx context.Context, tx *storeTx) error {
	now := time.Now().UnixNano()
	_, err := tx.ExecContext(ctx,
		`INSERT INTO teams (name, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO NOTHING`,
		string(DefaultTeam), now, now)
	return err
}

// CreateTeam registers a team. Registering is idempotent, because
// provisioning retries the whole team-creation path rather than half
// of it.
func (o *Operator) CreateTeam(ctx context.Context, team Team) error {
	if strings.TrimSpace(string(team)) == "" {
		return ErrNoTeam
	}
	now := time.Now().UnixNano()
	_, err := o.exec(ctx,
		`INSERT INTO teams (name, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO NOTHING`,
		string(team), now, now)
	return err
}

// ListTeams returns every registered team, name order.
func (o *Operator) ListTeams(ctx context.Context) (_ []Team, err error) {
	rows, err := o.query(ctx, `SELECT name FROM teams ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	var out []Team
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, Team(name))
	}
	return out, rows.Err()
}
