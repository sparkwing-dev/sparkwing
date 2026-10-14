package store

// safety: TranslateSchema preserves the neutral schema's constraints on both
// engines. SQLite uses the input unchanged; PostgreSQL maps BLOB to BYTEA.
// Unsupported dialects or syntax return an error rather than dropping clauses.
func TranslateSchema(neutral string, dialect Dialect) (string, error) {
	panic("unimplemented: neutral schema translation")
}

// safety: SchemaOwner makes deployment authority explicit even when an operator
// table carries an optional tenant for audit or binding checks.
type SchemaOwner string

const (
	SchemaTenant   SchemaOwner = "tenant"
	SchemaOperator SchemaOwner = "operator"
)

// safety: SchemaTable classifies every baseline table so new state cannot evade
// the tenant-predicate guard by omission.
type SchemaTable struct {
	Name  string
	Owner SchemaOwner
}

// safety: SchemaTables is the complete ownership inventory for schema.sql.
// Implementations must compare the created tables with this registry on both engines.
var SchemaTables = []SchemaTable{
	{Name: "accounts", Owner: SchemaOperator},
	{Name: "approvals", Owner: SchemaTenant},
	{Name: "billable_intervals", Owner: SchemaTenant},
	{Name: "billing_awards", Owner: SchemaTenant},
	{Name: "blob_members", Owner: SchemaTenant},
	{Name: "blob_receipts", Owner: SchemaTenant},
	{Name: "blob_records", Owner: SchemaTenant},
	{Name: "blob_tombstones", Owner: SchemaTenant},
	{Name: "blobs", Owner: SchemaTenant},
	{Name: "business_events", Owner: SchemaOperator},
	{Name: "capabilities", Owner: SchemaOperator},
	{Name: "card_attempts", Owner: SchemaTenant},
	{Name: "card_charges", Owner: SchemaTenant},
	{Name: "card_refunds", Owner: SchemaTenant},
	{Name: "card_spend_days", Owner: SchemaOperator},
	{Name: "child_invocations", Owner: SchemaTenant},
	{Name: "concurrency_entries", Owner: SchemaTenant},
	{Name: "concurrency_holders", Owner: SchemaTenant},
	{Name: "concurrency_waiters", Owner: SchemaTenant},
	{Name: "credit_charges", Owner: SchemaTenant},
	{Name: "credit_checkouts", Owner: SchemaTenant},
	{Name: "credit_freezes", Owner: SchemaTenant},
	{Name: "credit_grants", Owner: SchemaTenant},
	{Name: "cron_fires", Owner: SchemaTenant},
	{Name: "cron_schedules", Owner: SchemaTenant},
	{Name: "debug_pauses", Owner: SchemaTenant},
	{Name: "egress_day", Owner: SchemaOperator},
	{Name: "egress_usage", Owner: SchemaTenant},
	{Name: "eval_units", Owner: SchemaTenant},
	{Name: "events", Owner: SchemaTenant},
	{Name: "flow_nonces", Owner: SchemaOperator},
	{Name: "free_slots", Owner: SchemaTenant},
	{Name: "git_credential_machines", Owner: SchemaTenant},
	{Name: "git_credential_releases", Owner: SchemaTenant},
	{Name: "git_credentials", Owner: SchemaTenant},
	{Name: "github_app_automation", Owner: SchemaTenant},
	{Name: "github_app_deliveries", Owner: SchemaOperator},
	{Name: "github_app_extra_repos", Owner: SchemaTenant},
	{Name: "github_app_installations", Owner: SchemaTenant},
	{Name: "github_app_triggers", Owner: SchemaTenant},
	{Name: "github_runner_bindings", Owner: SchemaTenant},
	{Name: "identities", Owner: SchemaOperator},
	{Name: "identity_unlinks", Owner: SchemaOperator},
	{Name: "invitation_email_log", Owner: SchemaOperator},
	{Name: "invitations", Owner: SchemaTenant},
	{Name: "leases", Owner: SchemaOperator},
	{Name: "memberships", Owner: SchemaTenant},
	{Name: "node_attempts", Owner: SchemaTenant},
	{Name: "log_segments", Owner: SchemaTenant},
	{Name: "log_streams", Owner: SchemaTenant},
	{Name: "node_metrics", Owner: SchemaTenant},
	{Name: "node_steps", Owner: SchemaTenant},
	{Name: "nodes", Owner: SchemaTenant},
	{Name: "payment_events", Owner: SchemaTenant},
	{Name: "payment_warnings", Owner: SchemaOperator},
	{Name: "pipeline_profiles", Owner: SchemaTenant},
	{Name: "pipeline_success_pins", Owner: SchemaTenant},
	{Name: "pool_access_rules", Owner: SchemaTenant},
	{Name: "quota_admission", Owner: SchemaTenant},
	{Name: "quota_reservations", Owner: SchemaTenant},
	{Name: "repos", Owner: SchemaTenant},
	{Name: "run_effects", Owner: SchemaTenant},
	{Name: "run_rate_usage", Owner: SchemaTenant},
	{Name: "runs", Owner: SchemaTenant},
	{Name: "secrets", Owner: SchemaTenant},
	{Name: "sessions", Owner: SchemaTenant},
	{Name: "settings", Owner: SchemaOperator},
	{Name: "signup_admissions", Owner: SchemaOperator},
	{Name: "signup_gate", Owner: SchemaOperator},
	{Name: "sparkwing_schema_version", Owner: SchemaOperator},
	{Name: "storage_commit_receipts", Owner: SchemaTenant},
	{Name: "storage_month_usage", Owner: SchemaTenant},
	{Name: "storage_quotas", Owner: SchemaTenant},
	{Name: "storage_reservations", Owner: SchemaTenant},
	{Name: "storage_run_usage", Owner: SchemaTenant},
	{Name: "team_build_trust", Owner: SchemaTenant},
	{Name: "team_counters", Owner: SchemaTenant},
	{Name: "team_deletions", Owner: SchemaOperator},
	{Name: "team_download_day", Owner: SchemaTenant},
	{Name: "team_spend_days", Owner: SchemaTenant},
	{Name: "team_storage", Owner: SchemaTenant},
	{Name: "teams", Owner: SchemaOperator},
	{Name: "triggers", Owner: SchemaTenant},
	{Name: "uploads", Owner: SchemaTenant},
}
