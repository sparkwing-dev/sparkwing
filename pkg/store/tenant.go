package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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
// # The compiler does not protect you yet
//
// Every method ported to Tenant still exists, byte-identical, on
// *Store, because pkg/storage.StateStore and internal/backend.Backend
// name those methods and removing them one family at a time would break
// satisfaction mid-port. Go interface satisfaction is structural, so
// until that single atomic deletion lands a *Store substitutes for a
// *Tenant at every seam and a forgotten `team = ?` compiles, links,
// runs and passes review. What catches it instead is
// tenant_sql_scope_guard_test.go, which parses this package and fails
// on any statement that touches a tenant-owned table without a team
// predicate. Run it. Its allowlist is the list of statements not yet
// ported, and the entry a family owns must be deleted in the same
// commit that ports it.
//
// # Porting a method family
//
// The mechanical edits are not the whole job. Work the list:
//
//  1. Receiver: *Store becomes *Tenant, and the team argument, if the
//     method had one, goes away. Keep the *Store twin until the final
//     deletion; it writes [DefaultTeam].
//  2. WHERE: every clause gains `team = ?`, including the ones in
//     subqueries and in EXISTS.
//  3. INSERT: the column list gains `team`, and so does every VALUES
//     row.
//  4. ON CONFLICT: the column list is not enough. A conflict target on
//     caller-supplied values -- a name, a pipeline, a run id -- matches
//     another team's row, and a DO UPDATE then overwrites it while the
//     row keeps its original team. Put the team in the conflict guard
//     (`WHERE t.team = excluded.team`), and check RowsAffected: a guard
//     that refuses reports no error, so without the check the writer is
//     told it succeeded and then cannot read what it "created".
//     createRunTx in store.go is the worked example.
//  5. Uniqueness and indexes: the conflict target needs a unique index
//     to fire at all, and a scoped listing needs a team-leading index or
//     it scans the fleet. runs got idx_runs_team_started here; the next
//     table needs its own. Widening an existing unique index to lead with
//     team is its own change, not part of a method port.
//  6. Mutators that match nothing: a foreign id must report
//     [ErrNotFound], not nil. A tenant whose writes silently no-op while
//     its reads say not-found makes a cancel endpoint answer 200 and
//     cancel nothing. assertRunBelongsToTeamTx is the check to copy.
//  7. Helpers: a helper that still takes *Store and reads a tenant-owned
//     table is a hole the receiver change does not close. Give it a
//     `team Team` parameter and pass t.team. loadAgentLossRetry and
//     assertRunMutationFenceTx were both such holes.
//  8. Lookups that fail open: a lookup whose miss means "no limit"
//     inverts when it is scoped. [Store.StorageQuotaFor] returns an
//     unlimited quota on a miss and [StorageQuota] reads a zero limit as
//     unbounded, so adding `team = ?` to it against rows written before
//     the port makes every team unlimited, silently, with the suite
//     green. Scoping such a lookup needs a backfill in the same commit,
//     or a miss that fails closed. Find them before you scope them.
//
// # What stays unscoped
//
// Not every caller is a tenant. A method with both a user caller and a
// maintenance caller keeps an unscoped twin on [Operator] and gains a
// scoped one on Tenant.
//
//   - Reapers and sweeps run for the deployment. An expiry sweep that
//     only reaped one team would leave every other team's leases held.
//   - Claims stay on *Store but are never cross-team. ClaimNextReadyNode,
//     ClaimNamedNode, ClaimNextTriggerFor, ClaimSpecificTriggerFor and the
//     executor offer and award path take no team argument because the team
//     comes off the claimant's own token row (claimScope), never off the
//     caller or the request. A metered token is one team's like any other,
//     and no credential reads every team's queue. The placement hold counts
//     only live runners of the claim's team.
//   - Migration reads and writes every row by definition.
//
// tenant_runs.go is the worked example for the scoped half.
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

// ForTeam returns the handle through which tenant-owned rows are read
// and written. It rejects a team that is not registered, or whose
// deletion is pending, because nothing
// has a foreign key to teams: SQLite cannot add one to an existing table
// without rewriting all 34 of them, so the registry is enforced at the
// one place a handle is minted rather than 34 times in the schema. The
// cost is a read per handle and a window in which a team deleted after
// the check still has a live handle; deleting a team is an operator path
// that has to drain its rows anyway.
func (s *Store) ForTeam(ctx context.Context, team Team) (*Tenant, error) {
	team = NormalizeTeam(team)
	if team == "" {
		return nil, ErrNoTeam
	}
	var name string
	err := s.queryRow(ctx, `
		SELECT name FROM teams WHERE name = ?
		AND NOT EXISTS (SELECT 1 FROM team_deletions d WHERE d.slug = teams.name AND d.state = ?)`,
		string(team), TeamDeletionPending).Scan(&name)
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

// safety: the un-ported *Store surface reads and writes through this
// handle, because a caller predating the tenant key has no team to offer
// and the migration put every existing row in this one. It skips the
// registry read, which the migration's own registration makes redundant.
// safety: a row that belongs to a run takes the run's team in the statement
// that writes it, so no caller can hand it another; a row whose run is gone
// lands where every pre-tenant row did.
const runTeamSQL = `COALESCE((SELECT team FROM runs WHERE id = ?), '` + string(DefaultTeam) + `')`

func (s *Store) defaultTenant() *Tenant { return &Tenant{s: s, team: DefaultTeam} }

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

// ListRunsAcrossTeams returns runs from every team, newest first.
func (o *Operator) ListRunsAcrossTeams(ctx context.Context, f RunFilter) ([]*Run, error) {
	return o.s.listRuns(ctx, allTeams(), f)
}

// CountRunsAcrossTeams counts runs from every team, ignoring f's Limit.
func (o *Operator) CountRunsAcrossTeams(ctx context.Context, f RunFilter) (int, error) {
	return o.s.countRuns(ctx, allTeams(), f)
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
	"credit_checkouts",
	"credit_freezes",
	"credit_grants",
	"cron_fires",
	"cron_schedules",
	"debug_pauses",
	"egress_usage",
	"events",
	"free_slots",
	"git_credential_machines",
	"git_credential_releases",
	"git_credentials",
	"github_app_installations",
	"github_app_triggers",
	"github_runner_bindings",
	"github_runner_credentials",
	"github_webhook_bindings",
	"invitations",
	"memberships",
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

// safety: these tables are created after v49 with the team column already
// in their schema, so the v49 ladder step that adds the column to every
// tenant-owned table runs before they exist and skips them.
var keyedAtCreation = []string{
	"credit_checkouts", "credit_freezes", "free_slots", "git_credential_machines", "git_credential_releases",
	"git_credentials", "github_app_installations", "github_app_triggers",
	"github_runner_bindings", "github_runner_credentials", "invitations", "memberships",
}

// safety: executors is here because an executor enrolls with the deployment
// and is offered work from every team on it, and sparkwing_meta because the
// bag is the deployment's; the per-team keys inside that bag need a table of
// their own rather than a column on it. accounts and identities are here
// because a human belongs to the deployment and reaches teams through
// memberships, which are tenant-owned. signup_gate is the deployment's one
// gate for new accounts, and signup_admissions the deployment-wide admissions
// its velocity limits count.
var operatorTables = []string{
	"accounts",
	"executors",
	"github_app_connect_states",
	"github_app_deliveries",
	"identities",
	"signup_admissions",
	"signup_gate",
	"sparkwing_meta",
	"sparkwing_requirements",
	"invitation_email_log",
	"sparkwing_schema_version",
	"team_deletions",
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
		if slices.Contains(keyedAtCreation, table) {
			continue
		}
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
		if slices.Contains(keyedAtCreation, table) {
			continue
		}
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
	team = NormalizeTeam(team)
	if team == "" {
		return ErrNoTeam
	}
	now := time.Now().UnixNano()
	_, err := o.s.exec(ctx,
		`INSERT INTO teams (name, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO NOTHING`,
		string(team), now, now)
	return err
}

// ListTeams returns every registered team, name order.
func (o *Operator) ListTeams(ctx context.Context) (_ []Team, err error) {
	rows, err := o.s.query(ctx, `SELECT name FROM teams ORDER BY name`)
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
