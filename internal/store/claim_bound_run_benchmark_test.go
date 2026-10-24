package store

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func BenchmarkClaimBoundRun4000(b *testing.B) {
	for _, scenario := range []string{"first-match", "last-match"} {
		b.Run(scenario, func(b *testing.B) {
			ctx := b.Context()
			var examined atomic.Int64
			probe := fmt.Sprintf("bound_claim_probe_%x", time.Now().UnixNano())
			if err := sqlite.RegisterScalarFunction(probe, 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
				examined.Add(1)
				return int64(1), nil
			}); err != nil {
				b.Fatal(err)
			}
			base, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				b.Fatal(err)
			}
			trace := &claimBenchmarkTrace{}
			sql.Register(probe, &claimBenchmarkDriver{Driver: base.Driver(), probe: probe, trace: trace})
			if err := base.Close(); err != nil {
				b.Fatal(err)
			}
			s, err := openSQL(ctx, probe, sqliteDSN(filepath.Join(b.TempDir(), "claims.db")), DialectSQLite, "benchmark", nil)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := s.Close(); err != nil {
					b.Error(err)
				}
			})
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			exec := func(query string, args ...any) {
				b.Helper()
				if _, err := tx.ExecContext(ctx, query, args...); err != nil {
					b.Fatal(err)
				}
			}
			exec(`INSERT INTO teams(name,created_at,updated_at) VALUES ('bench',1,1)`)
			exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('bench','bound',1,'bench','running',?,'request',1,'gh:1','https://github.com/acme/repo','github.com/acme/repo','host','execute',?,'',1,'r30',30)`, []byte(`{"request":{"mode":"execute","dispatch":"host","options":{}},"provenance":{"trust":"asserted","pipeline":"bench","writer":"benchmark"}}`), []byte(`[]`))
			wrapped := &storeTx{tx: tx, dialect: DialectSQLite, store: s, operator: s.AsOperator()}
			keys := make(map[string]string)
			for _, label := range []string{"eligible", "unavailable"} {
				keys[label], err = persistClaimRequirementsTx(ctx, wrapped, "bench", []string{label})
				if err != nil {
					b.Fatal(err)
				}
			}
			want := "job-0000"
			if scenario == "last-match" {
				want = "job-3999"
			}
			for i := range 4000 {
				node := fmt.Sprintf("job-%04d", i)
				label := "eligible"
				execution := []byte(`{"requires":{"value":["eligible"]}}`)
				if scenario == "last-match" && node != want {
					label = "unavailable"
					execution = []byte(`{"requires":{"value":["unavailable"]}}`)
				}
				exec(`INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements) VALUES ('bench','bound',?,'pending',?, ?,?,?)`, node, i+1, []byte(`{}`), execution, keys[label])
			}
			facts := []claimFacts{{Prefix: "benchmark", Team: "bench", BindRun: "bound", FreeSlots: 1, Current: 1, Trust: "any", Kinds: []string{"node"}, Labels: []string{"eligible"}, Tier: tierSelfHosted}}
			template := claimSQL{dialect: DialectSQLite, facts: facts, teams: []claimTeamFacts{{Team: "bench"}}, kinds: []capability.UnitKind{capability.UnitNode}, now: 1700000000000000000}
			demand := UnitDemand{CapabilityPrefix: "benchmark", Kinds: template.kinds}
			poll := func() {
				b.Helper()
				query := template
				unit, err := searchClaimTx(ctx, wrapped, demand, &query)
				if err != nil || string(unit.Unit.NodeID) != want {
					b.Fatalf("selected %+v, error %v; want %s", unit, err, want)
				}
			}
			poll()
			samples := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for range b.N {
				start := time.Now()
				poll()
				samples = append(samples, time.Since(start))
			}
			b.StopTimer()
			trace.examine = true
			poll()
			trace.examine = false
			b.ReportMetric(float64(examined.Load()), "replayed_pending_predicate_evals/op")
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(4000, "ready_jobs")
		})
	}
}
