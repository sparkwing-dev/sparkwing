package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// safety: a row that belongs to a run takes the run's team in the statement that writes it, so no
// caller can hand it another; a row whose run is gone lands where every pre-tenant row did.
const runTeamSQL = `COALESCE((SELECT team FROM runs WHERE id = ?), '` + string(DefaultTeam) + `')`

// safety: the un-ported *Store surface goes through this handle, because a caller predating the tenant
// key has no team to offer and the migration put every existing row in this team.
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

// RunTeam reports which team owns the run runID, for a sweep that found the
// id across teams and has to act on it through that team's handle.
// [ErrNotFound] when no run carries the id.
//
// safety: runs and triggers are looked up apart, because two teams may each
// hold one of them under the same id, and a union would answer with either.
func (o *Operator) RunTeam(ctx context.Context, runID string) (Team, error) {
	var team string
	err := o.s.queryRow(ctx, `SELECT team FROM runs WHERE id = ?`, runID).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("run", runID)
	}
	return Team(team), err
}

// TriggerTeam reports which team owns the trigger triggerID, as RunTeam does
// for runs. [ErrNotFound] when no trigger carries the id.
func (o *Operator) TriggerTeam(ctx context.Context, triggerID string) (Team, error) {
	var team string
	err := o.s.queryRow(ctx, `SELECT team FROM triggers WHERE id = ?`, triggerID).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("trigger", triggerID)
	}
	return Team(team), err
}

// safety: a Store call that names a run and holds no handle acts in the run's
// own team, where the run's rows were written; an unknown run is the default
// team's, as every pre-tenant row is.
func (s *Store) runTenant(ctx context.Context, runID string) (*Tenant, error) {
	team, err := s.AsOperator().RunTeam(ctx, runID)
	if errors.Is(err, ErrNotFound) {
		return s.defaultTenant(), nil
	}
	if err != nil {
		return nil, err
	}
	return &Tenant{s: s, team: team}, nil
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

// safety: a table is on this list or on operatorTables and never on both,
// because a scoped query on an unclassified table cannot be written; the
// guard in tenant_classification_internal_test.go refuses either mistake.
var tenantTables = []string{
	"approvals",
	"business_events",
	"card_attempts",
	"card_charges",
	"card_refunds",
	"child_invocations",
	"claim_tokens",
	"concurrency_cache",
	"concurrency_entries",
	"concurrency_holders",
	"concurrency_waiters",
	"credit_charges",
	"credit_checkouts",
	"credit_freezes",
	"credit_grants",
	"data_objects",
	"cron_fires",
	"cron_schedules",
	"debug_pauses",
	"egress_usage",
	"events",
	"free_slots",
	"git_credential_machines",
	"git_credential_releases",
	"git_credentials",
	"github_app_automation",
	"github_app_extra_repos",
	"github_app_installations",
	"github_app_triggers",
	"github_runner_bindings",
	"github_runner_credentials",
	"invitations",
	"memberships",
	"node_execution_attempts",
	"node_log_segments",
	"node_logs",
	"node_metrics",
	"node_outputs",
	"node_steps",
	"nodes",
	"pipeline_profiles",
	"repos",
	"run_definition_plans",
	"runs",
	"secrets",
	"sessions",
	"storage_commit_receipts",
	"storage_month_usage",
	"storage_quotas",
	"storage_reservations",
	"storage_run_usage",
	"team_download_day",
	"team_spend_days",
	"team_storage",
	"team_build_trust",
	"uploads",
	"tokens",
	"trigger_claim_tokens",
	"triggers",
}

// safety: sparkwing_meta is the deployment's bag, so per-team keys need their own table. A human
// belongs to the deployment and reaches teams through tenant-owned memberships. signup_gate and
// signup_admissions gate new accounts deployment-wide.
var operatorTables = []string{
	"accounts",
	"card_spend_days",
	"github_app_connect_states",
	"github_app_deliveries",
	"identities",
	"identity_link_states",
	"identity_unlinks",
	"signup_admissions",
	"signup_gate",
	"sparkwing_meta",
	"invitation_email_log",
	"payment_warnings",
	"sparkwing_schema_version",
	"team_deletions",
	"teams",
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

// CreateTeam registers a team. Registering is idempotent, because
// provisioning retries the whole team-creation path rather than half
// of it.
func (o *Operator) CreateTeam(ctx context.Context, team Team) error {
	team = NormalizeTeam(team)
	if team == "" {
		return ErrNoTeam
	}
	return o.s.inTx(ctx, func(tx *storeTx) error { return registerTeamTx(ctx, tx, team) })
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
