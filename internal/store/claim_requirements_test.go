package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestClaimRequirementsCanonicalSetsAndUnsatisfiableTerms(t *testing.T) {
	s := authorityStore(t)
	persist := func(selectors []string) string {
		t.Helper()
		var id string
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			var err error
			id, err = persistClaimRequirementsTx(t.Context(), tx, "a", selectors)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if got := persist([]string{"", " \t\n", "\u2003"}); got != "" {
		t.Fatalf("empty requirements = %q", got)
	}
	rewriteSchemaCount(t, s.db, "claim_requirements", 0)
	first := persist([]string{" gpu , cpu, gpu ", "os=linux", "os=linux", " "})
	if first == "" || first != persist([]string{"os=linux", "cpu,gpu"}) {
		t.Fatal("equivalent requirement sets have different identities")
	}
	rewriteSchemaCount(t, s.db, "claim_requirements", 1)
	rewriteSchemaCount(t, s.db, "claim_requirement_terms", 3)
	impossible := persist([]string{" , , "})
	if impossible == "" || impossible == first {
		t.Fatal("unsatisfiable term collapsed into empty requirements")
	}
	var label string
	if err := s.db.QueryRow(`SELECT label FROM claim_requirement_terms WHERE team='a' AND requirements=?`, impossible).Scan(&label); err != nil || label != "" {
		t.Fatalf("unsatisfiable term label = %q: %v", label, err)
	}
}

func TestAcceptedWorkPersistsClaimRequirements(t *testing.T) {
	tenant, acceptance, _ := planFixtureWithInput(t, runspec.DispatchHost, func(input *IntakeRequest) {
		input.Request.Options.Requires.Optional = runspec.Optional[[]string]{Set: true, Value: []string{"linux"}}
	})
	var trigger string
	if err := tenant.s.db.QueryRow(`SELECT claim_requirements FROM triggers WHERE team=? AND id=?`, tenant.team, acceptance.RunID).Scan(&trigger); err != nil || trigger == "" {
		t.Fatalf("intake requirement set = %q: %v", trigger, err)
	}
	execution := acceptance.Seal.Resolved.Settings.Jobs["job"]
	execution.Requires.Optional = runspec.Optional[[]string]{Set: true, Value: []string{"linux"}}
	acceptance.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"body"}],"requires":["linux"]}]}`)
	acceptance.Seal.Resolved.Settings.Jobs["job"] = execution
	if _, err := tenant.AcceptPlan(t.Context(), acceptance); err != nil {
		t.Fatal(err)
	}
	var node string
	if err := tenant.s.db.QueryRow(`SELECT claim_requirements FROM nodes WHERE team=? AND run_id=? AND node_id='job'`, tenant.team, acceptance.RunID).Scan(&node); err != nil || node == "" || node != trigger {
		t.Fatalf("accepted job requirement set = %q, intake=%q: %v", node, trigger, err)
	}
	if err := tenant.s.db.QueryRow(`SELECT claim_requirements FROM triggers WHERE team=? AND id=?`, tenant.team, acceptance.RunID).Scan(&trigger); err != nil || trigger != node {
		t.Fatalf("resolved trigger requirement set = %q, job=%q: %v", trigger, node, err)
	}
}

func TestClaimIndexedRequirementsPreserveLabelSemantics(t *testing.T) {
	for _, test := range []struct {
		name      string
		selectors []string
		labels    []string
		want      bool
	}{
		{name: "quoted alternative", selectors: []string{`gpu, a"b`}, labels: []string{`a"b`}, want: true},
		{name: "escaped alternative", selectors: []string{`a\,gpu`}, labels: []string{`a\`}, want: true},
		{name: "json punctuation", selectors: []string{`a"],gpu`}, labels: []string{`a"]`}, want: true},
		{name: "alternatives", selectors: []string{" gpu, cpu , gpu ", "linux"}, labels: []string{"cpu", "linux"}, want: true},
		{name: "missing term", selectors: []string{"gpu,cpu", "linux"}, labels: []string{"cpu"}},
		{name: "unsatisfiable", selectors: []string{","}},
		{name: "whitespace", selectors: []string{" \t\n"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			execution, err := json.Marshal(runspec.JobExecutionSettings{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: test.selectors}}})
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, execution)
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			demand.Target, demand.Kinds, demand.Labels = nil, []capability.UnitKind{capability.UnitNode}, test.labels
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if test.want && (err != nil || claim.NodeID != "job") || !test.want && !errors.Is(err, ErrNotFound) {
				t.Fatalf("claim = %+v, want eligible=%v: %v", claim, test.want, err)
			}
		})
	}
}

func TestRetentionRemovesOnlyUnreferencedClaimRequirements(t *testing.T) {
	s, _, now := maintenanceStore(t)
	maintenanceRun(t, s, "old", 1, runstate.RunFailed)
	maintenanceRun(t, s, "retained", 2, runstate.RunFailed)
	old := now.Add(-120 * 24 * time.Hour).UnixNano()
	rewriteSchemaExec(t, s.db, `UPDATE runs SET finished_at=? WHERE id='old'`, old)
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)`, []byte(`90`))
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		for _, row := range []struct{ run, node, label string }{{"old", "shared", "gpu"}, {"retained", "shared", "gpu"}, {"old", "exclusive", "arm"}} {
			id, err := persistClaimRequirementsTx(t.Context(), tx, "a", []string{row.label})
			if err != nil {
				return err
			}
			if _, err := tx.execContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claim_requirements) VALUES ('a',?,?,'pending',?,?,?)`, row.run, row.node, []byte(`{}`), []byte(`{}`), id); err != nil {
				return err
			}
		}
		id, err := persistClaimRequirementsTx(t.Context(), tx, "a", []string{"linux"})
		if err != nil {
			return err
		}
		if _, err := tx.execContext(t.Context(), `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,finished_at,needs_labels,prefers_labels,claim_requirements) VALUES ('a','retained','done','trigger',1,1,1,?,?,?)`, []byte(`[]`), []byte(`[]`), id); err != nil {
			return err
		}
		_, err = persistClaimRequirementsTx(t.Context(), tx, "b", []string{"foreign"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		for _, table := range []string{"claim_requirements", "claim_requirement_terms"} {
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE team='a'`).Scan(&count); err != nil || count != want {
				t.Fatalf("%s count=%d want=%d: %v", table, count, want, err)
			}
		}
	}
	result, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 10})
	if err != nil || result.Runs != 1 {
		t.Fatalf("first retention = %+v: %v", result, err)
	}
	check(2)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET finished_at=? WHERE id='retained'`, old)
	result, err = s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 10})
	if err != nil || result.Runs != 1 {
		t.Fatalf("last-reference retention = %+v: %v", result, err)
	}
	check(0)
	rewriteSchemaCount(t, s.db, "claim_requirements", 1)
	rewriteSchemaCount(t, s.db, "claim_requirement_terms", 1)
}

func TestClaimRequirementReferencesProtectLiveWork(t *testing.T) {
	for _, table := range []string{"nodes", "triggers"} {
		t.Run(table, func(t *testing.T) {
			s, _ := unitClaimFixture(t)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET claim_context='',claim_requirements=''`)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET claim_context='',claim_requirements=''`)
			rewriteSchemaExec(t, s.db, `DELETE FROM claim_contexts`)
			rewriteSchemaExec(t, s.db, `DELETE FROM claim_requirements`)
			var ref sql.NullString
			if err := s.db.QueryRow(`SELECT claim_requirements_ref FROM ` + table).Scan(&ref); err != nil || ref.Valid {
				t.Fatalf("empty requirements reference = %+v: %v", ref, err)
			}
			if _, err := s.db.Exec(`UPDATE ` + table + ` SET claim_requirements='missing'`); err == nil {
				t.Fatal("work referenced a missing requirement set")
			}
			var id string
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				var err error
				id, err = persistClaimRequirementsTx(t.Context(), tx, "a", []string{"gpu"})
				if err != nil {
					return err
				}
				_, err = tx.execContext(t.Context(), `UPDATE `+table+` SET claim_requirements=?`, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT claim_requirements_ref FROM ` + table).Scan(&ref); err != nil || !ref.Valid || ref.String != id {
				t.Fatalf("requirements reference = %+v want %q: %v", ref, id, err)
			}
			if _, err := s.db.Exec(`DELETE FROM claim_requirements WHERE team='a' AND id=?`, id); err == nil {
				t.Fatal("removed requirements referenced by live work")
			}
			rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET claim_requirements=''`)
			rewriteSchemaExec(t, s.db, `DELETE FROM claim_requirements WHERE team='a' AND id=?`, id)
			rewriteSchemaCount(t, s.db, "claim_requirement_terms", 0)
		})
	}
}

func TestClaimPinnedSelectorsPreserveDecodedPrefixes(t *testing.T) {
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		t.Run(dialect.String(), func(t *testing.T) {
			driver, dsn := "sqlite", ":memory:"
			if dialect == DialectPostgres {
				driver, dsn = "pgx", os.Getenv("SPARKWING_TEST_PG_URL")
				if dsn == "" {
					t.Skip("PostgreSQL pin test requires SPARKWING_TEST_PG_URL")
				}
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			q := claimSQL{dialect: dialect}
			query := rewritePh(dialect, `WITH selector(value) AS (SELECT CAST(? AS TEXT)) SELECT `+q.pinned("selector.value")+` FROM selector`)
			for _, test := range []struct {
				name     string
				selector any
				want     bool
			}{
				{name: "missing"},
				{name: "empty", selector: `[]`},
				{name: "ordinary", selector: `["eligible"]`},
				{name: "size", selector: `["sparkwing-4cpu"]`},
				{name: "pool", selector: `["pool=gpu"]`, want: true},
				{name: "name", selector: `["name=runner"]`, want: true},
				{name: "class", selector: `["class=cloud"]`, want: true},
				{name: "alternatives", selector: `["gpu, name=runner"]`, want: true},
				{name: "ordinary alternatives", selector: `["ordinary, sparkwing-4cpu"]`},
				{name: "unicode trim", selector: `["\u2003class=cloud\u2003"]`, want: true},
				{name: "escaped equals", selector: `["name\u003drunner"]`, want: true},
				{name: "escaped prefix", selector: `["\u0070ool=gpu"]`, want: true},
				{name: "embedded prefix", selector: `["metadata-pool=gpu"]`},
				{name: "escaped ordinary", selector: `["ordinary\\label"]`},
			} {
				t.Run(test.name, func(t *testing.T) {
					var got bool
					if err := db.QueryRowContext(t.Context(), query, test.selector).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if got != test.want {
						t.Fatalf("pinned(%v) = %v, want %v", test.selector, got, test.want)
					}
				})
			}
		})
	}
}
