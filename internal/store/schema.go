package store

import (
	"errors"
	"strings"
)

// translateSchema preserves constraints and emits native scalar types and STRICT SQLite tables.
func translateSchema(neutral string, dialect Dialect) (string, error) {
	if dialect != DialectSQLite && dialect != DialectPostgres {
		return "", errors.New("unsupported schema dialect")
	}
	var out strings.Builder
	var previousWord string
	var tableName string
	var table bool
	depth := 0
	err := scanSQL(neutral, func(token string, kind sqlTokenKind) error {
		if kind == sqlWord {
			word := strings.ToUpper(token)
			if word == "TABLE" && previousWord == "CREATE" {
				table = true
			}
			if table && previousWord == "TABLE" {
				tableName = word
			}
			switch word {
			case "PRAGMA", "AUTOINCREMENT", "SERIAL", "BYTEA":
				return errors.New("schema contains non-neutral syntax")
			case "DATABASE_TIMESTAMP":
				token = auditRecordingTimeSQL(dialect)
			case "BIGINT":
				// INTEGER PRIMARY KEY aliases rowid and silently invents a NULL key.
				if dialect == DialectSQLite {
					token = "INT"
				}
			case "DOUBLE":
				if dialect == DialectSQLite {
					token = "REAL"
				}
			case "PRECISION":
				if dialect == DialectSQLite && previousWord == "DOUBLE" {
					token = ""
				}
			case "BLOB":
				if dialect == DialectPostgres {
					token = "BYTEA"
				}
			}
			previousWord = word
		}
		if token == "(" {
			depth++
		} else if token == ")" {
			depth--
			if table && depth == 0 {
				table = false
				if dialect == DialectSQLite {
					token += " STRICT"
					if tableName == "AUDIT_EVENTS" {
						// An implicit rowid would bypass the audit ID's replacement guard.
						token += ", WITHOUT ROWID"
					}
				}
			}
		} else if token == ";" {
			previousWord = ""
		}
		out.WriteString(token)
		return nil
	})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

func splitStatements(script string) ([]string, error) {
	var out []string
	start, offset := 0, 0
	hasSQL := false
	err := scanSQL(script, func(token string, kind sqlTokenKind) error {
		if token == ";" {
			if hasSQL {
				out = append(out, strings.TrimSpace(script[start:offset]))
			}
			start = offset + 1
			hasSQL = false
		} else if kind != sqlComment && strings.TrimSpace(token) != "" {
			hasSQL = true
		}
		offset += len(token)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hasSQL {
		out = append(out, strings.TrimSpace(script[start:]))
	}
	return out, nil
}

type sqlTokenKind int

const (
	sqlOther sqlTokenKind = iota
	sqlWord
	sqlComment
)

func scanSQL(neutral string, visit func(token string, kind sqlTokenKind) error) error {
	for i := 0; i < len(neutral); {
		start := i
		kind := sqlOther
		switch {
		case strings.HasPrefix(neutral[i:], "--"):
			kind = sqlComment
			for i < len(neutral) && neutral[i] != '\n' {
				i++
			}
		case strings.HasPrefix(neutral[i:], "/*"):
			kind = sqlComment
			end := strings.Index(neutral[i+2:], "*/")
			if end < 0 {
				return errors.New("unterminated SQL comment")
			}
			i += end + 4
		case neutral[i] == '\'' || neutral[i] == '"':
			quote := neutral[i]
			i++
			closed := false
			for i < len(neutral) {
				if neutral[i] == quote {
					i++
					if i < len(neutral) && neutral[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return errors.New("unterminated SQL quoted value")
			}
		case neutral[i] >= 'A' && neutral[i] <= 'Z' || neutral[i] >= 'a' && neutral[i] <= 'z' || neutral[i] == '_':
			kind = sqlWord
			for i < len(neutral) && ((neutral[i] >= 'A' && neutral[i] <= 'Z') || (neutral[i] >= 'a' && neutral[i] <= 'z') || (neutral[i] >= '0' && neutral[i] <= '9') || neutral[i] == '_') {
				i++
			}
		default:
			i++
		}
		if err := visit(neutral[start:i], kind); err != nil {
			return err
		}
	}
	return nil
}

// schemaOwner distinguishes tenant tables from deployment authority.
type schemaOwner string

const (
	schemaTenant   schemaOwner = "tenant"
	schemaOperator schemaOwner = "operator"
)

// schemaTable records the owner of a baseline table.
type schemaTable struct {
	Name                 string
	Owner                schemaOwner
	RetainOnTeamDeletion bool
}

// schemaTables classifies every table created by the neutral schema.
var schemaTables = []schemaTable{
	{Name: "accounts", Owner: schemaOperator},
	{Name: "approvals", Owner: schemaTenant},
	{Name: "billable_intervals", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "billing_awards", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "billing_pages", Owner: schemaTenant},
	{Name: "blob_members", Owner: schemaTenant},
	{Name: "blob_receipts", Owner: schemaTenant},
	{Name: "blob_records", Owner: schemaTenant},
	{Name: "blob_tombstones", Owner: schemaTenant},
	{Name: "blobs", Owner: schemaTenant},
	{Name: "audit_events", Owner: schemaOperator},
	{Name: "capabilities", Owner: schemaOperator},
	{Name: "card_refunds", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "claim_awards", Owner: schemaOperator},
	{Name: "claim_requirements", Owner: schemaTenant},
	{Name: "claim_contexts", Owner: schemaTenant},
	{Name: "claim_requirement_terms", Owner: schemaTenant},
	{Name: "concurrency_entries", Owner: schemaTenant},
	{Name: "concurrency_holders", Owner: schemaTenant},
	{Name: "admission_work", Owner: schemaTenant},
	{Name: "concurrency_waiters", Owner: schemaTenant},
	{Name: "credit_charges", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "credit_checkouts", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "credit_freezes", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "credit_grants", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "cron_fires", Owner: schemaTenant},
	{Name: "console_refund_receipts", Owner: schemaTenant, RetainOnTeamDeletion: true},
	{Name: "cron_schedules", Owner: schemaTenant},
	{Name: "debug_pauses", Owner: schemaTenant},
	{Name: "egress_usage", Owner: schemaTenant},
	{Name: "eval_units", Owner: schemaTenant},
	{Name: "events", Owner: schemaTenant},
	{Name: "flow_nonces", Owner: schemaOperator},
	{Name: "free_slots", Owner: schemaTenant},
	{Name: "git_credentials", Owner: schemaTenant},
	{Name: "github_app_automation", Owner: schemaTenant},
	{Name: "github_app_publications", Owner: schemaTenant},
	{Name: "github_app_deliveries", Owner: schemaOperator},
	{Name: "github_app_delivery_ids", Owner: schemaOperator},
	{Name: "github_app_delivery_effects", Owner: schemaOperator},
	{Name: "github_app_extra_repos", Owner: schemaTenant},
	{Name: "github_app_installations", Owner: schemaTenant},
	{Name: "github_app_triggers", Owner: schemaTenant},
	{Name: "github_runner_bindings", Owner: schemaTenant},
	{Name: "github_actions_exchanges", Owner: schemaTenant},
	{Name: "identities", Owner: schemaOperator},
	{Name: "identity_unlinks", Owner: schemaOperator},
	{Name: "invitation_email_log", Owner: schemaOperator},
	{Name: "invitations", Owner: schemaTenant},
	{Name: "leases", Owner: schemaOperator},
	{Name: "memberships", Owner: schemaTenant},
	{Name: "membership_epochs", Owner: schemaTenant},
	{Name: "overflow_controls", Owner: schemaTenant},
	{Name: "node_attempts", Owner: schemaTenant},
	{Name: "log_segments", Owner: schemaTenant},
	{Name: "log_streams", Owner: schemaTenant},
	{Name: "node_metrics", Owner: schemaTenant},
	{Name: "node_steps", Owner: schemaTenant},
	{Name: "nodes", Owner: schemaTenant},
	{Name: "payment_correlations", Owner: schemaTenant},
	{Name: "payment_events", Owner: schemaOperator},
	{Name: "payment_warnings", Owner: schemaOperator},
	{Name: "payment_refunds", Owner: schemaOperator},
	{Name: "payment_setups", Owner: schemaOperator},
	{Name: "payment_states", Owner: schemaOperator},
	{Name: "pipeline_profiles", Owner: schemaTenant},
	{Name: "pipeline_success_pins", Owner: schemaTenant},
	{Name: "pool_access_rules", Owner: schemaTenant},
	{Name: "quota_admission", Owner: schemaTenant},
	{Name: "quota_reservations", Owner: schemaTenant},
	{Name: "repos", Owner: schemaTenant},
	{Name: "run_control_requests", Owner: schemaTenant},
	{Name: "run_effects", Owner: schemaTenant},
	{Name: "run_rate_usage", Owner: schemaTenant},
	{Name: "runs", Owner: schemaTenant},
	{Name: "secrets", Owner: schemaTenant},
	{Name: "sessions", Owner: schemaOperator},
	{Name: "session_membership_epochs", Owner: schemaTenant},
	{Name: "settings", Owner: schemaOperator},
	{Name: "signup_admissions", Owner: schemaOperator},
	{Name: "signup_gate", Owner: schemaOperator},
	{Name: "sparkwing_schema_version", Owner: schemaOperator},
	{Name: "source_uploads", Owner: schemaTenant},
	{Name: "standing_requests", Owner: schemaTenant},
	{Name: "storage_month_usage", Owner: schemaTenant},
	{Name: "storage_quotas", Owner: schemaTenant},
	{Name: "storage_run_usage", Owner: schemaTenant},
	{Name: "team_build_trust", Owner: schemaTenant},
	{Name: "team_counters", Owner: schemaTenant},
	{Name: "team_deletions", Owner: schemaOperator},
	{Name: "team_download_day", Owner: schemaTenant},
	{Name: "team_first_success", Owner: schemaTenant},
	{Name: "team_storage", Owner: schemaTenant},
	{Name: "teams", Owner: schemaOperator},
	{Name: "triggers", Owner: schemaTenant},
	{Name: "uploads", Owner: schemaTenant},
}
