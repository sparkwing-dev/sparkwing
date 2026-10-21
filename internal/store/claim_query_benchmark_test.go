package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"modernc.org/sqlite"
)

func BenchmarkClaimQuery50000(b *testing.B) {
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		b.Run(dialect.String(), func(b *testing.B) {
			for _, scenario := range []string{"selective", "no-match", "skewed-team"} {
				b.Run(scenario, func(b *testing.B) { benchmarkClaimQuery(b, dialect, scenario) })
			}
		})
	}
}

func benchmarkClaimQuery(b *testing.B, dialect Dialect, scenario string) {
	ctx := b.Context()
	var db *sql.DB
	var err error
	var examined atomic.Int64
	probe := fmt.Sprintf("claim_probe_%x", time.Now().UnixNano())
	if dialect == DialectSQLite {
		if err = sqlite.RegisterScalarFunction(probe, 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			examined.Add(1)
			return int64(1), nil
		}); err != nil {
			b.Fatal(err)
		}
		db, err = sql.Open("sqlite", filepath.Join(b.TempDir(), "claims.db"))
	} else {
		dsn := os.Getenv("SPARKWING_TEST_PG_URL")
		if dsn == "" {
			b.Skip("PostgreSQL benchmark requires SPARKWING_TEST_PG_URL")
		}
		db, err = sql.Open("pgx", dsn)
	}
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	if dialect == DialectPostgres {
		schema := fmt.Sprintf("claim_bench_%x", time.Now().UnixNano())
		for _, query := range []string{`CREATE SCHEMA ` + schema, `SET search_path TO ` + schema} {
			if _, err = db.ExecContext(ctx, query); err != nil {
				b.Fatal(err)
			}
		}
		b.Cleanup(func() {
			if _, err := db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
				b.Error(err)
			}
		})
	}
	schema, err := TranslateSchema(schemaNeutral, dialect)
	if err != nil {
		b.Fatal(err)
	}
	statements, err := splitStatements(schema)
	if err != nil {
		b.Fatal(err)
	}
	for _, statement := range statements {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			b.Fatal(err)
		}
	}
	exec := func(query string, args ...any) {
		b.Helper()
		if _, err := db.ExecContext(ctx, rewritePh(dialect, query), args...); err != nil {
			b.Fatal(err)
		}
	}
	exec(`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`)
	request := []byte(`{"request":{"mode":"execute","dispatch":"host","options":{}},"provenance":{"trust":"asserted"}}`)
	for _, team := range []string{"a", "b"} {
		exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,?,1,'bench','running',?,'request',1,'gh:1','https://github.com/acme/repo','github.com/acme/repo','host','execute',?,'',1,'r30',30)`, team, "run-"+team, request, []byte(`[]`))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, rewritePh(dialect, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES (?,?,?,'pending',1,?,?)`))
	if err != nil {
		b.Fatal(err)
	}
	for i := range 50000 {
		team := "a"
		if scenario != "skewed-team" && i%2 == 1 || scenario == "skewed-team" && i == 49999 {
			team = "b"
		}
		required := "unavailable"
		if scenario != "no-match" && i == 49999 {
			required = "eligible"
		}
		body, _ := json.Marshal(map[string]any{"requires": map[string]any{"value": []string{required}}})
		if _, err = statement.ExecContext(ctx, team, "run-"+team, fmt.Sprintf("job-%05d", i), []byte(`{}`), body); err != nil {
			b.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		b.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	facts := []claimFacts{{Prefix: "benchmark", Current: 1, Cloud: 1, Trust: "any", Kinds: []string{"node"}, Labels: []string{"eligible"}, Tier: TierCloud}}
	teams := []claimTeamFacts{{Team: "a", Available: 128, Balance: 1e12}, {Team: "b", Available: 128, Balance: 1e12}}
	q := claimSQL{dialect: dialect}
	query, err := q.query(facts, teams, DefaultCreditRateTable(DefaultCreditRateMicro), []capability.UnitKind{capability.UnitNode}, UnitDemand{CapabilityPrefix: "benchmark"}, time.Unix(1700000000, 0).UnixNano())
	if err != nil {
		b.Fatal(err)
	}
	if dialect == DialectSQLite {
		query = strings.Replace(query, `n.status='pending'`, probe+`(n.node_id)=1 AND n.status='pending'`, 1)
	}
	query = rewritePh(dialect, query)
	b.ResetTimer()
	for range b.N {
		var team, run, node, kind string
		var generation int64
		err = db.QueryRowContext(ctx, query, q.args...).Scan(&team, &run, &node, &generation, &kind)
		if scenario == "no-match" {
			if err != sql.ErrNoRows {
				b.Fatalf("no-match: %v", err)
			}
		} else if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(50000, "ready_jobs")
	if dialect == DialectSQLite {
		b.ReportMetric(float64(examined.Load())/float64(b.N), "candidate_visits/op")
	} else {
		var raw []byte
		if err = db.QueryRowContext(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+query, q.args...).Scan(&raw); err != nil {
			b.Fatal(err)
		}
		var plans []map[string]any
		if err = json.Unmarshal(raw, &plans); err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(claimPlanVisits(plans[0]["Plan"].(map[string]any)), "candidate_visits/op")
		b.Logf("EXPLAIN ANALYZE: %s", raw)
	}
}

func claimPlanVisits(plan map[string]any) float64 {
	var visits float64
	if relation, _ := plan["Relation Name"].(string); relation == "nodes" {
		rows, _ := plan["Actual Rows"].(float64)
		removed, _ := plan["Rows Removed by Filter"].(float64)
		loops, _ := plan["Actual Loops"].(float64)
		visits += (rows + removed) * loops
	}
	if plans, ok := plan["Plans"].([]any); ok {
		for _, child := range plans {
			visits += claimPlanVisits(child.(map[string]any))
		}
	}
	return visits
}
