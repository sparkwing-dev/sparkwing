package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func BenchmarkClaimQuery50000(b *testing.B) {
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		b.Run(dialect.String(), func(b *testing.B) {
			for _, scenario := range []string{"selective", "no-match", "skewed-team", "broad", "tied", "ranked", "metered", "fresh-ranked", "fresh-broad-ranked"} {
				b.Run(scenario, func(b *testing.B) { benchmarkClaimQuery(b, dialect, scenario) })
			}
		})
	}
}

func benchmarkClaimQuery(b *testing.B, dialect Dialect, scenario string) {
	ctx := b.Context()
	freshRanked := scenario == "fresh-ranked" || scenario == "fresh-broad-ranked"
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
		db, err = sql.Open("sqlite", sqliteDSN(filepath.Join(b.TempDir(), "claims.db")))
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
	request := []byte(`{"request":{"mode":"execute","dispatch":"host","options":{}},"provenance":{"trust":"asserted"}}`)
	if scenario == "ranked" || scenario == "metered" || freshRanked {
		request = []byte(`{"request":{"mode":"execute","dispatch":"controller","options":{}},"provenance":{"trust":"asserted"}}`)
	}
	teams := make([]claimTeamFacts, 200)
	for i := range teams {
		team := fmt.Sprintf("team-%03d", i)
		exec(`INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, team)
		exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,?,1,'bench','running',?,'request',1,'gh:1','https://github.com/acme/repo','github.com/acme/repo','host','execute',?,'',1,'r30',30)`, team, "run-"+team, request, []byte(`[]`))
		teams[i] = claimTeamFacts{Team: team, Available: 128, Balance: 1e12}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	state := &Store{db: db, dialect: dialect}
	wrapped := &storeTx{tx: tx, dialect: dialect, store: state, operator: state.AsOperator(), clock: func(context.Context) (time.Time, error) { return time.Unix(1700000000, 0), nil }}
	keys := make(map[string]string)
	for _, team := range teams {
		for _, label := range []string{"eligible", "unavailable"} {
			key, err := persistClaimRequirementsTx(ctx, wrapped, Team(team.Team), []string{label})
			if err != nil {
				b.Fatal(err)
			}
			keys[team.Team+"/"+label] = key
		}
	}
	statement, err := tx.PrepareContext(ctx, rewritePh(dialect, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements) VALUES (?,?,?,'pending',?, ?,?,?)`))
	if err != nil {
		b.Fatal(err)
	}
	for i := range 50000 {
		team := teams[i%len(teams)].Team
		if scenario == "skewed-team" && i < 49999 {
			team = teams[0].Team
		}
		required := "unavailable"
		if scenario == "broad" || scenario == "tied" || scenario == "fresh-broad-ranked" || scenario != "no-match" && i == 49999 {
			required = "eligible"
		}
		body, _ := json.Marshal(map[string]any{"requires": map[string]any{"value": []string{required}}})
		arrived := int64(i + 1)
		if freshRanked {
			arrived += 1700000000000000000 - int64(time.Second)
		} else if scenario == "tied" {
			arrived = 1
		}
		if _, err = statement.ExecContext(ctx, team, "run-"+team, fmt.Sprintf("job-%05d", i), arrived, []byte(`{}`), body, keys[team+"/"+required]); err != nil {
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
	if scenario == "metered" {
		facts[0].Metered = 1
	}
	for i := range 999 {
		labels := []string{fmt.Sprintf("other-%04d", i)}
		if scenario == "fresh-broad-ranked" && i < len(teams) {
			labels = []string{"eligible"}
		}
		facts = append(facts, claimFacts{Prefix: fmt.Sprintf("observer-%04d", i), Team: teams[i%len(teams)].Team, ObservedAt: 1700000000000000000, Trust: "any", Kinds: []string{"node"}, Labels: labels, Tier: TierSelfHosted})
	}
	template := claimSQL{dialect: dialect, facts: facts, teams: teams, rates: DefaultCreditRateTable(DefaultCreditRateMicro), kinds: []capability.UnitKind{capability.UnitNode}, now: 1700000000000000000}
	demand := UnitDemand{CapabilityPrefix: "benchmark", Kinds: template.kinds}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	wrapped.tx = tx
	var result UnitRef
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for range b.N {
		start := time.Now()
		result, err = searchClaimTx(ctx, wrapped, demand, &template)
		samples = append(samples, time.Since(start))
		if scenario == "no-match" || scenario == "fresh-broad-ranked" {
			if !errors.Is(err, ErrNotFound) {
				b.Fatalf("%s: %v", scenario, err)
			}
		} else if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
	b.ReportMetric(50000, "ready_jobs")
	b.ReportMetric(200, "teams")
	b.ReportMetric(1000, "claimants")
	var askRows int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM unit_asks WHERE claimant='benchmark'`).Scan(&askRows); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(askRows), "ask_rows")
	if scenario == "fresh-broad-ranked" && askRows != 50000 {
		b.Fatalf("recorded %d asks; want every fresh eligible node", askRows)
	}
	routeQuery, routeArgs, err := template.routesSQL(freshRanked)
	if err != nil {
		b.Fatal(err)
	}
	var visits float64
	diagnose := func(name, query string, args []any) {
		b.Helper()
		if dialect == DialectSQLite {
			query = strings.ReplaceAll(query, `n.status='pending'`, probe+`(n.node_id)=1 AND n.status='pending'`)
			rows, err := tx.QueryContext(ctx, query, args...)
			if err != nil {
				b.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				b.Fatal(errors.Join(err, rows.Close()))
			}
			dest := make([]any, len(columns))
			for i := range dest {
				dest[i] = new(any)
			}
			for rows.Next() {
				if err = rows.Scan(dest...); err != nil {
					b.Fatal(errors.Join(err, rows.Close()))
				}
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				b.Fatal(err)
			}
			return
		}
		var raw []byte
		if err = tx.QueryRowContext(ctx, `EXPLAIN (ANALYZE,FORMAT JSON) `+rewritePh(dialect, query), args...).Scan(&raw); err != nil {
			b.Fatal(err)
		}
		var plans []map[string]any
		if err = json.Unmarshal(raw, &plans); err != nil {
			b.Fatal(err)
		}
		visits += claimPlanVisits(plans[0]["Plan"].(map[string]any))
		b.Logf("%s EXPLAIN ANALYZE: %s", name, raw)
	}
	fresh := claimSQL{dialect: dialect, freshOnly: true}
	clock := fresh.param(template.now)
	source := fresh.source(template.kinds)
	diagnose("ask preflight", `WITH clock(now) AS (SELECT CAST(`+clock+` AS BIGINT)) SELECT EXISTS (SELECT 1 FROM (`+source+`) ready LIMIT 1)`, fresh.args)
	if freshRanked {
		fresh := claimSQL{dialect: dialect, freshOnly: true, preferencesFree: true}
		if _, err := fresh.query(facts, teams, template.rates, template.kinds, demand, template.now); err != nil {
			b.Fatal(err)
		}
		diagnose("asks", fresh.askSQL, fresh.askArgs)
	}
	diagnose("routes", routeQuery, routeArgs)
	if result.RunID != "" {
		q := claimSQL{dialect: dialect}
		named := demand
		named.Target = &result
		query, err := q.query(facts, teams, template.rates, []capability.UnitKind{result.Kind}, named, template.now)
		if err != nil {
			b.Fatal(err)
		}
		diagnose("predicate", query, q.args)
	}
	if dialect == DialectSQLite {
		visits = float64(examined.Load())
	}
	// perf: these sampled queries omit rejected heads and their eligibility probes.
	b.ReportMetric(visits, "sampled_node_visits/op")
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
