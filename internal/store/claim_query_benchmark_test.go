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
			for _, scenario := range []string{"selective", "no-match", "skewed-team", "broad", "tied", "ranked", "metered", "fresh-ranked", "fresh-broad-ranked", "fresh-broad-alternatives", "fresh-broad-metered"} {
				b.Run(scenario, func(b *testing.B) { benchmarkClaimQuery(b, dialect, scenario) })
			}
		})
	}
}

func benchmarkClaimQuery(b *testing.B, dialect Dialect, scenario string) {
	ctx := b.Context()
	freshRanked := scenario == "fresh-ranked" || strings.HasPrefix(scenario, "fresh-broad-")
	var db *sql.DB
	var err error
	var examined, examinedContexts atomic.Int64
	var trace claimBenchmarkTrace
	probe := fmt.Sprintf("claim_probe_%x", time.Now().UnixNano())
	if dialect == DialectSQLite {
		if err = sqlite.RegisterScalarFunction(probe, 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			examined.Add(1)
			return int64(1), nil
		}); err != nil {
			b.Fatal(err)
		}
		contextProbe := probe + "_context"
		if err = sqlite.RegisterScalarFunction(contextProbe, 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			examinedContexts.Add(1)
			return int64(1), nil
		}); err != nil {
			b.Fatal(err)
		}
		base, openErr := sql.Open("sqlite", ":memory:")
		if openErr != nil {
			b.Fatal(openErr)
		}
		sql.Register(probe, &claimBenchmarkDriver{Driver: base.Driver(), probe: probe, contextProbe: contextProbe, trace: &trace})
		if err := base.Close(); err != nil {
			b.Fatal(err)
		}
		db, err = sql.Open(probe, sqliteDSN(filepath.Join(b.TempDir(), "claims.db")))
	} else {
		dsn := os.Getenv("SPARKWING_TEST_PG_URL")
		if dsn == "" {
			b.Skip("PostgreSQL benchmark requires SPARKWING_TEST_PG_URL")
		}
		base, openErr := sql.Open("pgx", dsn)
		if openErr != nil {
			b.Fatal(openErr)
		}
		sql.Register(probe, &claimBenchmarkDriver{Driver: base.Driver(), trace: &trace})
		if err := base.Close(); err != nil {
			b.Fatal(err)
		}
		db, err = sql.Open(probe, dsn)
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
	schema, err := translateSchema(schemaNeutral, dialect)
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
	request := []byte(`{"request":{"mode":"execute","dispatch":"host","options":{}},"provenance":{"trust":"asserted","pipeline":"bench","writer":"benchmark"}}`)
	if scenario == "ranked" || scenario == "metered" || freshRanked {
		request = []byte(`{"request":{"mode":"execute","dispatch":"controller","options":{}},"provenance":{"trust":"asserted","pipeline":"bench","writer":"benchmark"}}`)
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
	labelTerm := func(label string) string {
		if scenario == "fresh-broad-alternatives" && label == "eligible" {
			return "eligible,alternate"
		}
		return label
	}
	keys := make(map[string]string)
	contexts := make(map[string]string)
	for _, team := range teams {
		for _, label := range []string{"eligible", "unavailable"} {
			key, err := persistClaimRequirementsTx(ctx, wrapped, Team(team.Team), []string{labelTerm(label)})
			if err != nil {
				b.Fatal(err)
			}
			keys[team.Team+"/"+label] = key
			body, _ := json.Marshal(map[string]any{"requires": map[string]any{"value": []string{labelTerm(label)}}})
			context, err := persistClaimContextTx(ctx, wrapped, Team(team.Team), "run-"+team.Team, capability.UnitNode, key, body)
			if err != nil {
				b.Fatal(err)
			}
			contexts[team.Team+"/"+label] = context
		}
	}
	statement, err := tx.PrepareContext(ctx, rewritePh(dialect, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) VALUES (?,?,?,'pending',?, ?,?,?,?)`))
	if err != nil {
		b.Fatal(err)
	}
	for i := range 50000 {
		team := teams[i%len(teams)].Team
		if scenario == "skewed-team" && i < 49999 {
			team = teams[0].Team
		}
		required := "unavailable"
		if scenario == "broad" || scenario == "tied" || strings.HasPrefix(scenario, "fresh-broad-") || scenario != "no-match" && i == 49999 {
			required = "eligible"
		}
		body, _ := json.Marshal(map[string]any{"requires": map[string]any{"value": []string{labelTerm(required)}}})
		arrived := int64(i + 1)
		if freshRanked {
			arrived += 1700000000000000000 - int64(time.Second)
		} else if scenario == "tied" {
			arrived = 1
		}
		if _, err = statement.ExecContext(ctx, team, "run-"+team, fmt.Sprintf("job-%05d", i), arrived, []byte(`{}`), body, keys[team+"/"+required], contexts[team+"/"+required]); err != nil {
			b.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		b.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if dialect == DialectPostgres {
		for _, table := range []string{"teams", "runs", "nodes", "claim_contexts", "claim_requirements", "claim_requirement_terms", "triggers", "eval_units", "leases", "team_deletions", "capabilities"} {
			exec(`ANALYZE ` + table)
		}
	}
	facts := []claimFacts{{Prefix: "benchmark", FreeSlots: 1, Current: 1, Cloud: 1, Trust: "any", Kinds: []string{"node"}, Labels: []string{"eligible"}, Tier: TierCloud}}
	if scenario == "metered" || scenario == "fresh-broad-metered" {
		facts[0].Metered = 1
	}
	for i := range 999 {
		labels := []string{fmt.Sprintf("other-%04d", i)}
		if strings.HasPrefix(scenario, "fresh-broad-") && i < len(teams) {
			labels = []string{"eligible"}
			if scenario == "fresh-broad-alternatives" {
				labels = []string{"alternate"}
			}
		}
		facts = append(facts, claimFacts{Prefix: fmt.Sprintf("observer-%04d", i), FreeSlots: 1, Team: teams[i%len(teams)].Team, ObservedAt: 1700000000000000000, Trust: "any", Kinds: []string{"node"}, Labels: labels, Tier: tierSelfHosted})
	}
	template := claimSQL{dialect: dialect, facts: facts, teams: teams, rates: DefaultCreditRateTable(DefaultCreditRateMicro), kinds: []capability.UnitKind{capability.UnitNode}, now: 1700000000000000000}
	demand := UnitDemand{CapabilityPrefix: "benchmark", Kinds: template.kinds}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	wrapped.tx = tx
	poll := func(ctx context.Context) (UnitRef, error) {
		query := template
		var err error
		query.overflow, err = wrapped.operator.claimOverflowAccountsTx(ctx, wrapped, "", facts[0], nil)
		if err != nil {
			return UnitRef{}, err
		}
		return searchClaimTx(ctx, wrapped, demand, &query)
	}
	for range 5 {
		_, err = poll(ctx)
		if err != nil && !errors.Is(err, ErrNotFound) {
			b.Fatal(err)
		}
	}
	examined.Store(0)
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for iteration := range b.N {
		trace.enabled = dialect == DialectPostgres && iteration == b.N-1
		start := time.Now()
		queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = poll(queryCtx)
		cancel()
		trace.enabled = false
		samples = append(samples, time.Since(start))
		if scenario == "no-match" || strings.HasPrefix(scenario, "fresh-broad-") {
			if !errors.Is(err, ErrNotFound) {
				b.Fatalf("%s: %v", scenario, err)
			}
		} else if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if dialect == DialectSQLite {
		trace.examine = true
		_, err := poll(ctx)
		trace.examine = false
		if err != nil && !errors.Is(err, ErrNotFound) {
			b.Fatal(err)
		}
		if strings.HasPrefix(scenario, "fresh-broad-") && examined.Load() > int64(2*len(teams)) {
			b.Fatalf("held queue examined %d ready candidates; want at most two indexed heads per team", examined.Load())
		}
		b.ReportMetric(float64(examined.Load()), "replayed_pending_predicate_evals/op")
		b.ReportMetric(float64(examinedContexts.Load()), "replayed_context_predicate_evals/op")
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
	b.ReportMetric(50000, "ready_jobs")
	b.ReportMetric(200, "teams")
	b.ReportMetric(1000, "claimants")
	if dialect == DialectPostgres {
		var visits float64
		for _, statement := range trace.statements {
			var raw []byte
			if err := tx.QueryRowContext(ctx, `EXPLAIN (ANALYZE,FORMAT JSON) `+statement.query, statement.args...).Scan(&raw); err != nil {
				b.Fatal(err)
			}
			var plans []map[string]any
			if err := json.Unmarshal(raw, &plans); err != nil {
				b.Fatal(err)
			}
			visits += claimPlanVisits(plans[0]["Plan"].(map[string]any))
			b.Logf("post-search EXPLAIN ANALYZE replay: %s", raw)
		}
		b.ReportMetric(visits, "replayed_node_and_context_rows/op")
	}
}

// Count readiness and context predicate evaluations across discovery and search.
// Latency excludes placement hydration and the post-timing instrumentation replay.
type claimBenchmarkDriver struct {
	driver.Driver
	probe        string
	runProbe     string
	contextProbe string
	trace        *claimBenchmarkTrace
}

func (d *claimBenchmarkDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return &claimBenchmarkConn{Conn: conn, probe: d.probe, runProbe: d.runProbe, contextProbe: d.contextProbe, trace: d.trace}, nil
}

type claimBenchmarkConn struct {
	queueLocked bool
	driver.Conn
	probe        string
	runProbe     string
	contextProbe string
	trace        *claimBenchmarkTrace
}

func (c *claimBenchmarkConn) instrument(query string) string {
	if c.probe == "" || !c.trace.examine || c.trace.lockedOnly && !c.queueLocked {
		return query
	}
	query = strings.ReplaceAll(query, `n.status='pending'`, c.probe+`(n.node_id)=1 AND n.status='pending'`)
	if c.contextProbe != "" {
		query = strings.ReplaceAll(query, `FROM claim_contexts cc,clock WHERE`, `FROM claim_contexts cc,clock WHERE `+c.contextProbe+`(cc.id)=1 AND`)
		query = strings.ReplaceAll(query, `WHERE cc.unit_kind IN`, `WHERE `+c.contextProbe+`(cc.id)=1 AND cc.unit_kind IN`)
		query = strings.ReplaceAll(query, `FROM overflow_controls WHERE`, `FROM overflow_controls WHERE `+c.contextProbe+`(account_id)=1 AND`)
		query = strings.ReplaceAll(query, `FROM claim_contexts WHERE`, `FROM claim_contexts WHERE `+c.contextProbe+`(overflow_account)=1 AND`)
		query = strings.ReplaceAll(query, `cc.team=?`, c.contextProbe+`(cc.overflow_account)=1 AND cc.team=?`)
	}
	if c.runProbe != "" {
		query = strings.ReplaceAll(query, `FROM runs WHERE`, `FROM runs WHERE `+c.runProbe+`(id)=1 AND`)
		query = strings.ReplaceAll(query, `FROM runs br WHERE`, `FROM runs br WHERE `+c.runProbe+`(br.id)=1 AND`)
		query = strings.ReplaceAll(query, `FROM runs r WHERE`, `FROM runs r WHERE `+c.runProbe+`(r.id)=1 AND`)
	}
	return query
}

func (c *claimBenchmarkConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(c.instrument(query))
}

func (c *claimBenchmarkConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.queueLocked = false
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (c *claimBenchmarkConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(query, "SELECT ordinal FROM claim_awards WHERE singleton=1") {
		c.queueLocked = true
	}
	c.trace.record(query, args)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, c.instrument(query), args)
}

func (c *claimBenchmarkConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.trace.record(query, args)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, c.instrument(query), args)
}

type claimBenchmarkStatement struct {
	query string
	args  []any
}

type claimBenchmarkTrace struct {
	lockedOnly bool
	examine    bool
	enabled    bool
	statements []claimBenchmarkStatement
}

func (trace *claimBenchmarkTrace) record(query string, args []driver.NamedValue) {
	if trace == nil || !trace.enabled || !strings.Contains(query, "nodes n") && !strings.Contains(query, "claim_contexts") && !strings.Contains(query, "overflow_controls") {
		return
	}
	statement := claimBenchmarkStatement{query: query, args: make([]any, len(args))}
	for i, arg := range args {
		statement.args[i] = arg.Value
	}
	trace.statements = append(trace.statements, statement)
}

func claimPlanVisits(plan map[string]any) float64 {
	var visits float64
	if relation, _ := plan["Relation Name"].(string); relation == "nodes" || relation == "claim_contexts" {
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
