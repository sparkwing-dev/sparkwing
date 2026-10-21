package store_test

import (
	"database/sql"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestNonBillingSchemaRejectsCounterexamples(t *testing.T) {
	st := storetest.Open(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st, q), args...); err != nil {
			t.Fatalf("seed %s: %v", q, err)
		}
	}
	exec(`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`)
	exec(`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','a@example.com',1,1)`)
	exec(`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','owner',1)`)
	exec(`INSERT INTO identities(provider,subject,account_id,created_at) VALUES ('github','1','account',1)`)
	exec(`INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('session','account','',1,2,'a','account')`)
	exec(`INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (1,'a',1,'a','Organization',1,1),(2,'b',2,'b','Organization',1,1)`)
	exec(`INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES ('a',1,'a/repo',1,'account',1,'gh:1')`)
	exec(`INSERT INTO github_app_triggers(team,repository_id,pipeline,repository,installation_id,created_at,repo_key) VALUES ('a',1,'p','a/repo',1,1,'gh:1')`)
	exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'p','pending',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{}`), []byte(`[]`))
	exec(`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, []byte(`{}`), []byte(`{}`))
	exec(`INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run','job',1,1,'holder','prefix','runner','runner','remote',1,2,'success')`)
	exec(`INSERT INTO blobs(team,digest,size,created_at) VALUES ('a','digest',1,1)`)
	exec(`INSERT INTO blob_receipts(team,id,run_id,node_id,attempt,generation,uploader_kind,digest,uploader_principal,capability_prefix,created_at) VALUES ('a','receipt','run','job',1,1,'job','digest','runner','prefix',1)`)
	exec(`INSERT INTO blob_receipts(team,id,run_id,uploader_kind,unit_kind,unit_id,unit_generation,digest,uploader_principal,capability_prefix,created_at) VALUES ('a','owner-receipt','run','owner_unit','plan','unit',1,'digest','runner','prefix',1)`)
	exec(`INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','key',1,1)`)
	exec(`INSERT INTO secrets(team,name,value,principal,created_at,updated_at) VALUES ('a','secret','sealed','account',1,1)`)
	exec(`INSERT INTO storage_quotas(team,storage_allowance_bytes,updated_at) VALUES ('a',1,1)`)
	exec(`INSERT INTO team_build_trust(team,trust_local_builds) VALUES ('a',0)`)
	exec(`INSERT INTO egress_usage(team,period,replica_id,bytes,updated_at) VALUES ('a','day','replica',1,1)`)
	exec(`INSERT INTO team_counters(team) VALUES ('a')`)
	for _, tc := range []struct{ name, query string }{
		{"null installation identity", `UPDATE github_app_installations SET installation_id=NULL WHERE installation_id=2`},
		{"foreign installation automation", `UPDATE github_app_automation SET installation_id=2 WHERE team='a'`},
		{"foreign installation trigger", `UPDATE github_app_triggers SET installation_id=2 WHERE team='a'`},
		{"orphan installation trigger", `UPDATE github_app_triggers SET installation_id=999 WHERE team='a'`},
		{"invented receipt attempt", `UPDATE blob_receipts SET attempt=999 WHERE id='receipt'`},
		{"invented receipt generation", `UPDATE blob_receipts SET generation=999 WHERE id='receipt'`},
		{"negative owner receipt generation", `UPDATE blob_receipts SET unit_generation=-1 WHERE id='owner-receipt'`},
		{"zero owner receipt generation", `UPDATE blob_receipts SET unit_generation=0 WHERE id='owner-receipt'`},
		{"orphan membership", `UPDATE memberships SET account_id='missing' WHERE team='a'`},
		{"orphan identity", `UPDATE identities SET account_id='missing' WHERE subject='1'`},
		{"orphan session", `UPDATE sessions SET account_id='missing' WHERE hash='session'`},
		{"null concurrency key", `UPDATE concurrency_entries SET key=NULL WHERE team='a'`},
		{"empty concurrency key", `UPDATE concurrency_entries SET key='' WHERE team='a'`},
		{"negative concurrency capacity", `UPDATE concurrency_entries SET capacity=-1 WHERE team='a'`},
		{"invalid build trust flag", `UPDATE team_build_trust SET trust_local_builds=2 WHERE team='a'`},
		{"negative egress", `UPDATE egress_usage SET bytes=-1 WHERE team='a'`},
		{"invalid secret flag", `UPDATE secrets SET masked=2 WHERE team='a'`},
		{"invalid identity flag", `UPDATE identities SET linked=2 WHERE subject='1'`},
		{"negative storage allowance", `UPDATE storage_quotas SET storage_allowance_bytes=-1 WHERE team='a'`},
		{"malformed scalar", `UPDATE teams SET next_run_number='garbage' WHERE name='a'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.DB().ExecContext(t.Context(), tc.query); err == nil {
				t.Fatalf("accepted invalid state: %s", tc.query)
			}
		})
	}
	if st.Dialect() == store.DialectSQLite {
		t.Run("fractional counter", func(t *testing.T) {
			if _, err := st.DB().Exec(`UPDATE team_counters SET concurrent_nodes=0.5 WHERE team='a'`); err == nil {
				t.Fatal("accepted fractional integer counter")
			}
		})
	}
}

func TestSQLiteBaselineRequiresStrictStorageForEveryTable(t *testing.T) {
	st := storetest.OpenSQLite(t)
	rows, err := st.DB().Query(`SELECT name, strict FROM pragma_table_list WHERE schema='main' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var name string
		var strict int
		if err := rows.Scan(&name, &strict); err != nil {
			t.Fatal(err)
		}
		if strict != 1 {
			t.Errorf("table %s accepts non-strict scalars", name)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no tables checked")
	}
}

func TestAccountRelationsCascadeOnDeletion(t *testing.T) {
	st := storetest.Open(t)
	for _, q := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('gone','gone@example.com',1,1)`,
		`INSERT INTO identities(provider,subject,account_id,created_at) VALUES ('github','gone','gone',1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','gone','reader',1)`,
		`INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('gone','gone','',1,2,'default','gone')`,
		`DELETE FROM accounts WHERE id='gone'`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"identities", "memberships", "sessions"} {
		var count int
		err := st.DB().QueryRow(`SELECT count(*) FROM ` + table + ` WHERE account_id='gone'`).Scan(&count)
		if err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s retained orphan account relations", table)
		}
	}
}
