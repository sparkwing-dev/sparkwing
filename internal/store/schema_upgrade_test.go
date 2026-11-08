package store_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func schema30Store(t *testing.T, target *storetest.Target) {
	t.Helper()
	st := target.Open(t)
	dropActionsSchema(t, st)
	for _, statement := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('kept',1,1)`,
		`INSERT INTO secrets(team,name,value,principal,created_at,updated_at) VALUES ('kept','TOKEN','sealed-configuration','fixture',1,1)`,
		`DROP INDEX idx_runs_live_children`,
		`DROP TABLE local_runner_instances`,
		`DROP TABLE local_run_secrets`,
		`DROP TABLE local_machine_admission`,
		`UPDATE sparkwing_schema_version SET version=30, baseline_digest='d0f42d8e52dd4ce999ace44a3767b6302d87e54c3b79dd544dfc5cd1ff8c0cf4'`,
	} {
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaUpgradeAddsLiveChildIndexToVersion33(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	dropActionsSchema(t, st)
	if _, err := st.DB().Exec(`DROP INDEX idx_runs_live_children`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=33, baseline_digest='b4ac52448bafd4ea378d1ed2e1d9aec0c610d3a5e27df4a7a780c178f425a47b'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("schema upgrade differs: %s", shapeDiff(a, b))
	}
}

func TestSchemaUpgradeAddsLocalRunnersToVersion32(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	dropActionsSchema(t, st)
	if _, err := st.DB().Exec(`DROP INDEX idx_runs_live_children`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE local_runner_instances`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=32, baseline_digest='8229dac48bd08544fb5b2cddfe2f9c9d0f0f3c41968eda4db1591aff9f247f61'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("schema upgrade differs: %s", shapeDiff(a, b))
	}
}

func TestSchemaUpgradeAddsLocalSecretsToVersion31(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	dropActionsSchema(t, st)
	if _, err := st.DB().Exec(`DROP INDEX idx_runs_live_children`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE local_runner_instances`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE local_run_secrets`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=31, baseline_digest='c09c6196b47e8c6561edd4f6274c105e5c8d00a8cbe9ec8925c5e0f23a68d236'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("schema upgrade differs: %s", shapeDiff(a, b))
	}
}

func TestSchemaUpgradeAddsMachineAdmissionToVersion30(t *testing.T) {
	target := storetest.New(t)
	schema30Store(t, target)
	st := target.Open(t)
	var secret string
	if err := st.DB().QueryRow(`SELECT value FROM secrets WHERE team='kept' AND name='TOKEN'`).Scan(&secret); err != nil || secret != "sealed-configuration" {
		t.Fatalf("configured secret after upgrade = %q, %v", secret, err)
	}
	if state, err := st.LoadMachineAdmission(t.Context()); err != nil || state != nil {
		t.Fatalf("upgraded machine state = %s, %v; want absent", state, err)
	}
	if err := st.SaveMachineAdmission(t.Context(), []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, st), schemaShape(t, fresh); a != b {
		t.Fatalf("upgraded schema differs from a fresh one:\n%s", shapeDiff(a, b))
	}
}

func TestSchemaUpgradeRefusesEveryOtherStore(t *testing.T) {
	for name, tc := range map[string]struct {
		statement string
		want      []string
	}{
		"unknown 30 digest": {`UPDATE sparkwing_schema_version SET baseline_digest='other'`, []string{`"other" is not the v0.69.2 baseline`, "left the store unchanged", "open it once with Sparkwing v0.69.2"}},
		"missing 30 digest": {`UPDATE sparkwing_schema_version SET baseline_digest=''`, []string{`"<missing>" is not the v0.69.2 baseline`, "left the store unchanged"}},
		"older than 30":     {`UPDATE sparkwing_schema_version SET version=29`, []string{"version 29 is older", "this build cannot upgrade it", "back up the store"}},
		"newer than binary": {fmt.Sprintf(`UPDATE sparkwing_schema_version SET version=%d`, store.ExpectedSchemaVersion()+1), []string{fmt.Sprintf("version %d is newer", store.ExpectedSchemaVersion()+1), "start a binary matching the store"}},
	} {
		t.Run(name, func(t *testing.T) {
			target := storetest.New(t)
			schema30Store(t, target)
			location := target.Path()
			dialect := store.DialectSQLite
			if location == "" {
				location, dialect = target.DSN(), store.DialectPostgres
			}
			mutate(t, dialect, location, tc.statement)
			st, err := target.TryOpen()
			if err == nil {
				_ = st.Close()
				t.Fatal("opened a store this binary must refuse")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q omits %q", err, want)
				}
			}
			if store.ExecRawForTest(t.Context(), dialect, location, `SELECT singleton FROM local_machine_admission`) == nil {
				t.Fatal("a refused store was upgraded")
			}
		})
	}
}

func TestPostgresSchemaUpgradeRunsOnceUnderConcurrentOpens(t *testing.T) {
	target := storetest.NewPostgres(t)
	schema30Store(t, target)
	const openers = 8
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := target.TryOpen()
			if err == nil {
				err = st.Close()
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("opener %d: %v", i, err)
		}
	}
	var rows, version int
	if err := target.Open(t).DB().QueryRow(`SELECT COUNT(*), MAX(version) FROM sparkwing_schema_version`).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || version != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version rows %d at %d, want one at %d", rows, version, store.ExpectedSchemaVersion())
	}
}

func mutate(t *testing.T, dialect store.Dialect, location, statement string) {
	t.Helper()
	if err := store.ExecRawForTest(t.Context(), dialect, location, statement); err != nil {
		t.Fatal(err)
	}
}

// safety: column order counts, because a migration that adds a column mid-table
// leaves upgraded stores unlike fresh ones.
func schemaShape(t *testing.T, st *store.Store) string {
	t.Helper()
	queries := []string{
		`SELECT m.name || '.' || p.cid || '.' || p.name || '.' || p.type || '.' || p."notnull" || '.' || COALESCE(p.dflt_value, '') FROM sqlite_master m JOIN pragma_table_xinfo(m.name) p WHERE m.type = 'table' ORDER BY m.name, p.cid`,
		`SELECT type || ' ' || name || ' ' || tbl_name FROM sqlite_master WHERE type <> 'table' AND name NOT LIKE 'sqlite_%' ORDER BY type, name`,
		`SELECT name || ' ' || sql FROM sqlite_master WHERE type = 'view' ORDER BY name`,
	}
	if st.Dialect() == store.DialectPostgres {
		queries = []string{
			`SELECT table_name || '.' || ordinal_position || '.' || column_name || '.' || data_type || '.' || is_nullable || '.' || COALESCE(column_default, '') || '.' || COALESCE(generation_expression, '') FROM information_schema.columns WHERE table_schema = current_schema() ORDER BY table_name, ordinal_position`,
			`SELECT conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid) FROM pg_constraint WHERE connamespace = current_schema()::regnamespace ORDER BY 1`,
			`SELECT indexname || ' ' || tablename FROM pg_indexes WHERE schemaname = current_schema() ORDER BY 1`,
			`SELECT table_name || ' ' || view_definition FROM information_schema.views WHERE table_schema = current_schema() ORDER BY 1`,
			`SELECT event_object_table || ' ' || trigger_name || ' ' || event_manipulation FROM information_schema.triggers WHERE trigger_schema = current_schema() ORDER BY 1`,
		}
	}
	var out []string
	for _, q := range queries {
		rows, err := st.DB().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out = append(out, line)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return strings.Join(out, "\n")
}

func shapeDiff(a, b string) string {
	left, right := strings.Split(a, "\n"), strings.Split(b, "\n")
	seen := map[string]int{}
	for _, l := range right {
		seen[l]++
	}
	var out []string
	for _, l := range left {
		if seen[l] > 0 {
			seen[l]--
			continue
		}
		out = append(out, "upgraded only: "+l)
	}
	for l, n := range seen {
		for range n {
			out = append(out, "fresh only: "+l)
		}
	}
	return strings.Join(out, "\n")
}

func dropActionsSchema(t *testing.T, st *store.Store) {
	t.Helper()
	for _, table := range []string{"github_actions_dispatches", "github_actions_approvals"} {
		if _, err := st.DB().Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchemaUpgradeAddsActionsWithoutLosingConfiguration(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	if _, err := st.DB().Exec(`INSERT INTO teams(name,created_at,updated_at) VALUES('kept',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO secrets(team,name,value,principal,created_at,updated_at) VALUES('kept','TOKEN','sealed-configuration','fixture',1,1)`); err != nil {
		t.Fatal(err)
	}
	dropActionsSchema(t, st)
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version=34,baseline_digest='187573d27274e9bf1796a84d502b39ac9e8c0cb7730e41143a1e4f586d5a6012'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	var secret string
	if err := upgraded.DB().QueryRow(`SELECT value FROM secrets WHERE team='kept' AND name='TOKEN'`).Scan(&secret); err != nil || secret != "sealed-configuration" {
		t.Fatalf("configured secret changed: %v", err)
	}
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("upgrade shape: %s", shapeDiff(a, b))
	}
}
