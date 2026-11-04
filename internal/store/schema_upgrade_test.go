package store_test

import (
	_ "embed"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

//go:embed testdata/schema-28-v0.68.3.sql
var schemaV0683 string

func releasedRows(dialect store.Dialect) []string {
	blob := func(text string) string {
		if dialect == store.DialectPostgres {
			return `'\x` + hex.EncodeToString([]byte(text)) + `'::bytea`
		}
		return `X'` + hex.EncodeToString([]byte(text)) + `'`
	}
	return []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('up',1,1)`,
		`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,git_sha,provenance_sha,repo_key,dispatch,intake_mode,created_subject_kind,created_principal,created_at,started_at,finished_at,log_retention_class,log_retention_days)
			VALUES ('up','r1',1,'gate','success',` + blob(`{}`) + `,'r1',1,'abc','abc','gh:1','host','execute','account','fixture',1,1,1,'r30',30)`,
		`INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES ('up','r1','a','done','success',1,` + blob(`{"id":"a"}`) + `,` + blob(`{}`) + `)`,
		`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('up','r1','b','pending',` + blob(`{"id":"b"}`) + `,` + blob(`{}`) + `)`,
	}
}

func releasedStore(t *testing.T, target *storetest.Target) {
	t.Helper()
	location := target.Path()
	dialect := store.DialectSQLite
	if location == "" {
		location, dialect = target.DSN(), store.DialectPostgres
	}
	if err := store.CreateReleasedStore(t.Context(), dialect, location, schemaV0683, 28, releasedRows(dialect)...); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaUpgradesAReleasedV0683StoreInPlace(t *testing.T) {
	target := storetest.New(t)
	releasedStore(t, target)
	st := target.Open(t)
	var version int
	var digest string
	if err := st.DB().QueryRow(`SELECT version, baseline_digest FROM sparkwing_schema_version`).Scan(&version, &digest); err != nil {
		t.Fatal(err)
	}
	fresh := storetest.New(t).Open(t)
	if version != store.ExpectedSchemaVersion() || digest != readSchemaBaselineDigest(t, fresh) {
		t.Fatalf("upgraded version %d digest %q, want %d %q", version, digest, store.ExpectedSchemaVersion(), readSchemaBaselineDigest(t, fresh))
	}
	rows, err := st.DB().Query(`SELECT node_id, status, trusted_host FROM nodes WHERE team='up' AND run_id='r1' ORDER BY node_id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var node, status string
		var trusted int
		if err := rows.Scan(&node, &status, &trusted); err != nil {
			t.Fatal(err)
		}
		got = append(got, node+" "+status+" "+string(rune('0'+trusted)))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a done 0,b pending 0" {
		t.Fatalf("upgraded node rows = %v, want both kept and untrusted", got)
	}
	if _, err := st.DB().Exec(`UPDATE nodes SET trusted_host=2 WHERE node_id='a'`); err == nil {
		t.Fatal("the upgraded column accepts a value outside 0 and 1")
	}
	if a, b := schemaShape(t, st), schemaShape(t, fresh); a != b {
		t.Fatalf("upgraded schema differs from a fresh one:\n%s", shapeDiff(a, b))
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// safety: a second open of the upgraded store must not run the step again.
	target.Open(t)
}

func TestSchemaUpgradeRefusesEveryOtherStore(t *testing.T) {
	for name, tc := range map[string]struct {
		statement string
		want      []string
	}{
		"unknown 28 digest": {`UPDATE sparkwing_schema_version SET baseline_digest='other'`, []string{`"other" is not the v0.68.3 baseline`, "left the store unchanged", "open it once with Sparkwing v0.68.3"}},
		"missing 28 digest": {`UPDATE sparkwing_schema_version SET baseline_digest=''`, []string{`"<missing>" is not the v0.68.3 baseline`, "left the store unchanged"}},
		"older than 28":     {`UPDATE sparkwing_schema_version SET version=27`, []string{"version 27 is older", "this build cannot upgrade it", "back up the store"}},
		"newer than binary": {`UPDATE sparkwing_schema_version SET version=31`, []string{"version 31 is newer", "start a binary matching the store"}},
	} {
		t.Run(name, func(t *testing.T) {
			target := storetest.New(t)
			releasedStore(t, target)
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
			if store.ExecRawForTest(t.Context(), dialect, location, `SELECT trusted_host FROM nodes`) == nil {
				t.Fatal("a refused store was upgraded")
			}
		})
	}
}

func TestPostgresSchemaUpgradeRunsOnceUnderConcurrentOpens(t *testing.T) {
	target := storetest.NewPostgres(t)
	releasedStore(t, target)
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

// safety: KEDA reads warm_pool_desired through a manually granted role, so replacing the view must keep its grants.
func TestPostgresSchemaUpgradeKeepsWarmViewGrants(t *testing.T) {
	target := storetest.NewPostgres(t)
	releasedStore(t, target)
	mutate(t, store.DialectPostgres, target.DSN(), `GRANT SELECT ON warm_pool_desired TO PUBLIC`)
	st := target.Open(t)
	var granted int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM information_schema.role_table_grants WHERE table_schema = current_schema() AND table_name = 'warm_pool_desired' AND grantee = 'PUBLIC' AND privilege_type = 'SELECT'`).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted != 1 {
		t.Fatal("upgrading the store revoked the warm_pool_desired grant")
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
