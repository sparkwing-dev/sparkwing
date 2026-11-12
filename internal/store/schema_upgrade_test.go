package store_test

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

const buildTrustSchema35 = `CREATE TABLE team_build_trust (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0 CHECK(trust_local_builds IN (0,1))
)`

func addSchema35BuildTrust(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.DB().Exec(`ALTER TABLE nodes DROP COLUMN group_wait_started_at`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(buildTrustSchema35); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaUpgradeRemovesBuildTrustFromVersion36AndPreservesConfiguredInstall(t *testing.T) {
	target := storetest.New(t)
	cipher := drillCipher(t)
	st := target.Open(t, store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{Intake: storetest.IntakePolicy{}, SessionKeys: cipher}, nil
	}))
	populateInstall(t, st, cipher)
	for _, statement := range []string{
		`INSERT INTO local_runner_instances(id,capability_prefix,machine,pid,process_birth,version,build_identity,state) SELECT 'kept-runner',prefix,'kept-machine',1,'kept-birth','kept-version','kept-build','active' FROM capabilities WHERE subject_id='runner-1'`,
		`INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES(35,'default',35,'kept-owner','Organization',1,1)`,
		`INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_at,repo_key,installation_id) VALUES('default',35,35,'kept-owner/repo',1,'github.com/kept-owner/repo',35)`,
		`INSERT INTO github_actions_approvals(team,repository_id,workflow,runner,workflow_ref,approval_id) VALUES('default',35,'worker.yml','ubuntu-latest','refs/heads/worker','kept-approval')`,
		`INSERT INTO github_actions_dispatches(receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,runner,policy_revision,state) VALUES('kept-receipt','default','drill-run-0','node-0',1,1,'kept-approval',35,35,35,'kept-owner/repo','worker.yml','refs/heads/worker','ubuntu-latest','kept-policy','accepted')`,
		`INSERT INTO cron_schedules(id,repo_path,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,team,repo_key,github_installation_id,github_repository_id) VALUES('kept-schedule','/repo','nightly','0 * * * *','UTC','skip',0,1,1,1,'default','github.com/kept-owner/repo',35,35)`,
	} {
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{
		`SELECT * FROM teams ORDER BY name`, `SELECT * FROM secrets ORDER BY name`,
		`SELECT * FROM capabilities ORDER BY prefix`, `SELECT * FROM local_runner_instances ORDER BY id`,
		`SELECT * FROM github_app_installations ORDER BY installation_id`, `SELECT * FROM github_runner_bindings ORDER BY repository_id`,
		`SELECT * FROM github_actions_approvals ORDER BY approval_id`, `SELECT * FROM github_actions_dispatches ORDER BY receipt`,
		`SELECT * FROM cron_schedules ORDER BY id`, `SELECT * FROM runs ORDER BY id`, `SELECT * FROM nodes ORDER BY node_id`,
	}
	rowsBefore := snapshotRows(t, st, queries)
	before := fingerprintInstall(t, st, cipher)
	store.PrepareSchema36ForTest(t, st)
	if _, err := st.DB().Exec(`INSERT INTO team_build_trust(team,trust_local_builds) VALUES ('default',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	if got := snapshotRows(t, upgraded, queries); got != rowsBefore {
		t.Fatal("configured rows changed during schema 36 upgrade")
	}
	after := fingerprintInstall(t, upgraded, cipher)
	before.Schema = store.ExpectedSchemaVersion()
	if before.String() != after.String() {
		t.Fatal("configured install changed during schema upgrade")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM local_runner_instances WHERE id='kept-runner' AND machine='kept-machine' AND build_identity='kept-build' AND state='active'`,
		`SELECT COUNT(*) FROM teams WHERE name='default'`,
		`SELECT COUNT(*) FROM github_app_installations WHERE installation_id=35 AND account_login='kept-owner'`,
		`SELECT COUNT(*) FROM github_runner_bindings WHERE repository_id=35 AND installation_id=35`,
		`SELECT COUNT(*) FROM github_actions_approvals WHERE approval_id='kept-approval' AND workflow_ref='refs/heads/worker'`,
		`SELECT COUNT(*) FROM github_actions_dispatches WHERE receipt='kept-receipt' AND state='accepted' AND policy_revision='kept-policy'`,
		`SELECT COUNT(*) FROM cron_schedules WHERE id='kept-schedule' AND cron='0 * * * *' AND github_installation_id=35`,
	} {
		var count int
		if err := upgraded.DB().QueryRow(query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("configured record after upgrade: count=%d, error=%v, query=%s", count, err, query)
		}
	}
	if _, err := upgraded.DB().Exec(`SELECT * FROM team_build_trust`); err == nil {
		t.Fatal("schema 37 retained build trust")
	}
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("schema upgrade differs: %s", shapeDiff(a, b))
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	if got := snapshotRows(t, target.Open(t), queries); got != rowsBefore {
		t.Fatal("reopen changed configured rows")
	}
}

func schema30Store(t *testing.T, target *storetest.Target) {
	t.Helper()
	st := target.Open(t)
	dropActionsSchema(t, st)
	for _, statement := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('kept',1,1)`,
		`INSERT INTO secrets(team,name,value,principal,created_at,updated_at) VALUES ('kept','TOKEN','sealed-configuration','fixture',1,1)`,
		buildTrustSchema35,
		`ALTER TABLE nodes DROP COLUMN group_wait_started_at`,
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
	addSchema35BuildTrust(t, st)
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
	addSchema35BuildTrust(t, st)
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
	addSchema35BuildTrust(t, st)
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
		"unknown 35 digest": {`UPDATE sparkwing_schema_version SET version=35,baseline_digest='other'`, []string{`"other" is not the schema 35 baseline`, "left the store unchanged"}},
		"missing 35 digest": {`UPDATE sparkwing_schema_version SET version=35,baseline_digest=''`, []string{`"<missing>" is not the schema 35 baseline`, "left the store unchanged"}},
		"unknown 36 digest": {`UPDATE sparkwing_schema_version SET version=36,baseline_digest='other'`, []string{`"other" is not the schema 36 baseline`, "left the store unchanged"}},
		"missing 36 digest": {`UPDATE sparkwing_schema_version SET version=36,baseline_digest=''`, []string{`"<missing>" is not the schema 36 baseline`, "left the store unchanged"}},
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
		`SELECT m.name || ' ' || f.id || ' ' || f.seq || ' ' || f."table" || ' ' || f."from" || ' ' || f."to" || ' ' || f.on_update || ' ' || f.on_delete FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f WHERE m.type='table' ORDER BY m.name,f.id,f.seq`,
		`SELECT m.name || '.' || p.cid || '.' || p.name || '.' || p.type || '.' || p."notnull" || '.' || COALESCE(p.dflt_value, '') FROM sqlite_master m JOIN pragma_table_xinfo(m.name) p WHERE m.type = 'table' ORDER BY m.name, p.cid`,
		`SELECT m.name || '.' || p."table" || '.' || p."from" || '.' || p."to" || '.' || p.on_delete FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) p WHERE m.type='table' ORDER BY 1`,
		`SELECT type || ' ' || name || ' ' || tbl_name FROM sqlite_master WHERE type <> 'table' AND name NOT LIKE 'sqlite_%' ORDER BY type, name`,
		`SELECT name || ' ' || sql FROM sqlite_master WHERE type = 'view' ORDER BY name`,
	}
	if st.Dialect() == store.DialectPostgres {
		// safety: fixture downgrades leave dropped-column ordinal gaps in Postgres; compare visible column order.
		queries = []string{
			`SELECT table_name || '.' || row_number() OVER (PARTITION BY table_name ORDER BY ordinal_position) || '.' || column_name || '.' || data_type || '.' || is_nullable || '.' || COALESCE(column_default, '') || '.' || COALESCE(generation_expression, '') FROM information_schema.columns WHERE table_schema = current_schema() ORDER BY table_name, ordinal_position`,
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
	addSchema35BuildTrust(t, st)
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

func TestSchemaUpgrade36PreservesActionsRows(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	addSchema35BuildTrust(t, st)
	dropActionsSchema(t, st)
	store.InstallSchema35ActionsForTest(t, st)
	for _, statement := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES('kept',1,1)`,
		`INSERT INTO secrets(team,name,value,principal,created_at,updated_at) VALUES('kept','TOKEN','sealed-configuration','fixture',1,1)`,
		`INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES(41,'kept',7,'acme','Organization',1,1)`,
		`INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_at,repo_key,installation_id) VALUES('kept',42,7,'acme/repo',1,'gh:42',41)`,
		`INSERT INTO github_actions_approvals VALUES('kept',42,'.github/workflows/ci.yaml','ubuntu-latest','refs/heads/main','approval')`,
		`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES('kept','run',1,'ci','pending',X'7b7d','hash',1,'gh:42','controller','execute','owner',1,'standard',7)`,
		`INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('kept','run','job','pending',1,X'7b7d',X'7b7d')`,
		`INSERT INTO github_actions_dispatches VALUES('receipt','kept','run','job',1,2,'approval',41,42,7,'acme/repo','.github/workflows/ci.yaml','refs/heads/main','revision','ubuntu-latest','policy','accepted','kept reason',12,'provider-run','2',1)`,
		`UPDATE sparkwing_schema_version SET version=35,baseline_digest='4d4bc4dc64181ee22e7d1bc02925b4587dde2395469658c9b06b6c6ee59a81a2'`,
	} {
		if st.Dialect() == store.DialectPostgres {
			statement = strings.ReplaceAll(statement, "X'7b7d'", "decode('7b7d','hex')")
		}
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{
		`SELECT * FROM teams ORDER BY name`, `SELECT * FROM secrets ORDER BY name`,
		`SELECT * FROM github_app_installations ORDER BY installation_id`, `SELECT * FROM github_runner_bindings ORDER BY repository_id`,
		`SELECT * FROM github_actions_approvals ORDER BY approval_id`, `SELECT * FROM github_actions_dispatches ORDER BY receipt`,
		`SELECT * FROM runs ORDER BY id`, `SELECT * FROM nodes ORDER BY node_id`,
	}
	before := snapshotRows(t, st, queries)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	if after := snapshotRows(t, upgraded, queries); before != after {
		t.Fatalf("upgrade changed rows:\nbefore %s\nafter %s", before, after)
	}
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("upgrade shape: %s", shapeDiff(a, b))
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := target.Open(t)
	if after := snapshotRows(t, reopened, queries); before != after {
		t.Fatal("reopen changed preserved rows")
	}
}

func snapshotRows(t *testing.T, st *store.Store, queries []string) string {
	t.Helper()
	var out strings.Builder
	for _, query := range queries {
		rows, err := st.DB().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			var stable []any
			for i, col := range cols {
				if col != "group_wait_started_at" {
					stable = append(stable, values[i])
				}
			}
			fmt.Fprintln(&out, stable)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

func TestSchemaUpgradeAddsGroupWaitClockToVersion37(t *testing.T) {
	target := storetest.New(t)
	f := newStoreRecoveryFixtureAt(t, false, target)
	before, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`ALTER TABLE nodes DROP COLUMN group_wait_started_at`, `UPDATE sparkwing_schema_version SET version=37,baseline_digest='7da3f341c2c5b6cc6fd60afb1a881979dfc5b64e79589a551522c655b5099e64'`} {
		if _, err := f.s.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s = target.Open(t, store.WithPolicies(sealStorePolicies))
	f.tenant, err = f.s.ForTeam(t.Context(), f.tenant.Team())
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("schema 37 job changed: before=%+v after=%+v err=%v", before, after, err)
	}
	var clocks int
	if err := f.s.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM nodes WHERE group_wait_started_at IS NOT NULL`).Scan(&clocks); err != nil || clocks != 0 {
		t.Fatalf("upgrade invented wait clocks: count=%d err=%v", clocks, err)
	}
}
