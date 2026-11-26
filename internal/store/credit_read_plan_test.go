package store

import (
	"database/sql"
	"strings"
	"testing"
)

// SeedCreditHistoryForTest gives team charges ledger rows, one reservation and three later charges per award, beside 46
// teams with 300 rows each, and refreshes planner statistics.
func SeedCreditHistoryForTest(t *testing.T, s *Store, team string, charges int) {
	t.Helper()
	const others = 46
	statements := []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`,
		`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i<?)
		 INSERT INTO teams(name,created_at,updated_at) SELECT 'other-'||i,1,1 FROM seq`,
		`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i<?)
		 INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,settled_at)
		 SELECT t.name,'res-'||i,'cap-'||i,'run-'||(i/4),'node-'||i,1,t.name,1,'cloud','at_award','node','job',i,CASE WHEN i<=2 THEN NULL ELSE i+60 END
		 FROM seq JOIN teams t ON t.name=? OR (t.name LIKE 'other-%' AND i<=75)`,
		`WITH RECURSIVE seq(j) AS (SELECT 0 UNION ALL SELECT j+1 FROM seq WHERE j<3)
		 INSERT INTO credit_charges(payment_mode,id,run_id,node_id,capability_prefix,kind,seconds,amount_micro,cpu_class,charged_at,team)
		 SELECT 'test',a.team||'-'||a.reservation_id||'-'||j,a.run_id,a.node_id,a.capability_prefix,
		  CASE j WHEN 0 THEN 'reservation' WHEN 1 THEN 'slice' WHEN 2 THEN 'usage' ELSE 'storage' END,
		  60,a.awarded_at*7%900000+j,2,a.awarded_at+j,a.team
		 FROM billing_awards a, seq`,
	}
	args := [][]any{{team}, {others}, {charges / 4, team}, nil}
	for i, statement := range statements {
		if _, err := s.db.ExecContext(t.Context(), rewritePh(s.dialect, statement), args[i]...); err != nil {
			t.Fatal(err)
		}
	}
	refresh := []string{`ANALYZE`}
	if s.dialect == DialectPostgres {
		// safety: an index-only scan is costed on the visibility map, which autovacuum keeps current in production.
		refresh = []string{`VACUUM ANALYZE credit_charges`, `VACUUM ANALYZE billing_awards`}
	}
	for _, statement := range refresh {
		if _, err := s.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

// CreditReadPlansForTest returns the plan of each credit read a claim poll makes for team, keyed by name. On
// PostgreSQL, generic asks for the plan a prepared statement reuses after its first executions.
func CreditReadPlansForTest(t *testing.T, s *Store, team string, generic bool) map[string]string {
	t.Helper()
	reads := map[string]struct {
		query string
		args  []string
	}{
		"balance":           {creditBalanceSQL, []string{team, "test", team, "test"}},
		"grant split":       {creditGrantSplitSQL, []string{team, "test", creditGrantReversal, team, "test", creditGrantReversal, team, "test"}},
		"active Cloud vCPU": {activeCloudCPUSQL, []string{"test", team}},
	}
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	exec := func(statement string) {
		if _, err := conn.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	plans := map[string]string{}
	for name, read := range reads {
		if s.dialect != DialectPostgres {
			args := make([]any, len(read.args))
			for i, arg := range read.args {
				args[i] = arg
			}
			plans[name] = explainLines(t, conn, `EXPLAIN QUERY PLAN `+read.query, args...)
			continue
		}
		literals := make([]string, len(read.args))
		for i, arg := range read.args {
			literals[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
		}
		mode := "force_custom_plan"
		if generic {
			mode = "force_generic_plan"
		}
		exec(`SET plan_cache_mode = ` + mode)
		exec(`PREPARE credit_read AS ` + rewritePh(s.dialect, read.query))
		plans[name] = explainLines(t, conn, `EXPLAIN EXECUTE credit_read(`+strings.Join(literals, ",")+`)`)
		exec(`DEALLOCATE credit_read`)
		exec(`RESET plan_cache_mode`)
	}
	return plans
}

// safety: SQLite's EXPLAIN QUERY PLAN returns four columns and PostgreSQL's EXPLAIN one; the detail is the last.
func explainLines(t *testing.T, conn *sql.Conn, query string, args ...any) string {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		fields := make([]any, len(columns))
		for i := range fields {
			fields[i] = new(any)
		}
		var line string
		fields[len(fields)-1] = &line
		if err := rows.Scan(fields...); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
