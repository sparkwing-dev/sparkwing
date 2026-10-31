package store

import (
	"database/sql"
	"testing"
)

func rewriteSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	rewriteSchemaExec(t, db, "PRAGMA foreign_keys = ON")
	schema, err := translateSchema(schemaNeutral, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, db, schema)
	return db
}

type rewriteSchemaExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func rewriteSchemaExec(t *testing.T, db rewriteSchemaExecer, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
	projectFixtureClaimRepositories(t, db, DialectSQLite, query)
}

func rewriteSchemaRefuses(t *testing.T, db rewriteSchemaExecer, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err == nil {
		t.Fatalf("accepted invalid row: %s", query)
	}
}

func rewriteSchemaCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d rows, want %d", table, got, want)
	}
}

func TestRewriteSchemaClassifiesEveryCreatedTable(t *testing.T) {
	db := rewriteSchemaDB(t)
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		actual[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	for _, table := range schemaTables {
		if !actual[table.Name] {
			t.Fatalf("missing or duplicate registered table %q", table.Name)
		}
		delete(actual, table.Name)
		if table.Owner == schemaOperator {
			continue
		}
		if table.Owner != schemaTenant {
			t.Fatalf("unrecognized ownership for %s: %q", table.Name, table.Owner)
		}
		var required int
		var defaultValue sql.NullString
		if err := db.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info(?) WHERE name = 'team'`, table.Name).Scan(&required, &defaultValue); err != nil {
			t.Fatalf("%s: %v", table.Name, err)
		}
		if required != 1 || defaultValue.Valid {
			t.Fatalf("%s: tenant is not explicit and required", table.Name)
		}
		var teamFKs int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list(?) WHERE "table" = 'teams' AND "from" = 'team' AND "to" = 'name'`, table.Name).Scan(&teamFKs); err != nil {
			t.Fatal(err)
		}
		if teamFKs != 1 {
			t.Fatalf("%s: got %d tenant foreign keys", table.Name, teamFKs)
		}
	}
	if len(actual) != 0 {
		t.Fatalf("unclassified tables: %v", actual)
	}
}

func TestRewriteSchemaRetentionPreservesFinancialRecordsAndNewestSuccess(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	for i, id := range []string{"old", "new"} {
		rewriteSchemaExec(t, db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,
repo_key,dispatch,intake_mode,created_principal,created_at,finished_at,log_retention_class,log_retention_days)
VALUES ('a',?,?,'p','success',?,'request',1,'gh:1','host','execute','account',1,2,'r30',30)`, id, i+1, []byte(`{"version":1}`))
	}
	rewriteSchemaExec(t, db, "INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','old','n','pending',?,?)", []byte(`{"version":1}`), []byte(`{}`))
	rewriteSchemaRefuses(t, db, "INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('b','old','n','pending',?,?)", []byte(`{"version":1}`), []byte(`{}`))
	for _, attempt := range []int{1, 2} {
		rewriteSchemaExec(t, db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome)
VALUES ('a','old','n',?,1,'holder','cap','runner','runner','remote',1,2,'success')`, attempt)
	}
	rewriteSchemaRefuses(t, db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at)
VALUES ('a','old','n',1,2,'holder','cap','runner','runner','remote',1)`)
	rewriteSchemaExec(t, db, "INSERT INTO credit_grants(reference,id,kind,amount_micro,created_at,team) VALUES ('g','g','paid',100,1,'a')")
	rewriteSchemaExec(t, db, `INSERT INTO credit_charges(id,run_id,node_id,capability_prefix,seconds,amount_micro,charged_at,team)
VALUES ('c','old','n','cap',1,10,1,'a')`)
	rewriteSchemaExec(t, db, `INSERT INTO storage_run_usage(team,run_id,period,updated_at) VALUES ('a','old','p',1)`)
	rewriteSchemaExec(t, db, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at)
VALUES ('a','res','cap','old',1,'a',1,'cloud','at_start','node','job',1)`)
	rewriteSchemaExec(t, db, `INSERT INTO billable_intervals(team,id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through)
VALUES ('a','interval','old',1,'cap','res','a',1,'cloud','node','job','at_start',1,2)`)
	rewriteSchemaExec(t, db, "INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','old','github_check','success',1,1)")
	rewriteSchemaExec(t, db, "INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES ('a','p','old',2)")
	rewriteSchemaRefuses(t, db, "DELETE FROM runs WHERE id = 'old'")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rewriteSchemaExec(t, tx, "UPDATE pipeline_success_pins SET run_id = 'new' WHERE team = 'a' AND pipeline = 'p'")
	rewriteSchemaExec(t, tx, "DELETE FROM runs WHERE id = 'old'")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nodes", "node_attempts", "run_effects"} {
		rewriteSchemaCount(t, db, table, 0)
	}
	for _, table := range []string{"runs", "pipeline_success_pins", "credit_grants", "credit_charges", "storage_run_usage", "billing_awards", "billable_intervals"} {
		rewriteSchemaCount(t, db, table, 1)
	}
	var balance int64
	if err := db.QueryRow("SELECT (SELECT sum(amount_micro) FROM credit_grants) - (SELECT sum(amount_micro) FROM credit_charges)").Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 90 {
		t.Fatalf("run deletion changed balance: got %d, want 90", balance)
	}
}

func TestRewriteSchemaReservationsKeepSettlementIdentity(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	rewriteSchemaExec(t, db, "INSERT INTO quota_admission(team,quota,period,updated_at) VALUES ('a','download','day',1)")
	rewriteSchemaExec(t, db, "INSERT INTO quota_reservations(team,id,quota,period,amount,expires_at,state) VALUES ('a','r','download','day',10,20,'held')")
	rewriteSchemaRefuses(t, db, "INSERT INTO quota_reservations(team,id,quota,period,amount,expires_at,state) VALUES ('b','r','download','day',10,20,'held')")
	rewriteSchemaRefuses(t, db, "UPDATE quota_reservations SET used = 11 WHERE id = 'r'")
	rewriteSchemaExec(t, db, "UPDATE quota_reservations SET state = 'committed', used = 8 WHERE team = 'a' AND id = 'r' AND state = 'held'")
	result, err := db.Exec("UPDATE quota_reservations SET state = 'released' WHERE team = 'a' AND id = 'r' AND state = 'held'")
	if err != nil {
		t.Fatal(err)
	}
	n, err := result.RowsAffected()
	if err != nil || n != 0 {
		t.Fatalf("repeat settlement changed %d rows: %v", n, err)
	}
}

func TestRewriteSchemaReceiptsSeparateJobAndNonJobUploaders(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	rewriteSchemaExec(t, db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,
repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days)
VALUES ('a','r',1,'p','pending',?,'request',1,'gh:1','host','publish','account',1,'r30',30)`, []byte(`{"version":1}`))
	rewriteSchemaExec(t, db, "INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','r','n','pending',?,?)", []byte(`{"version":1}`), []byte(`{}`))
	rewriteSchemaExec(t, db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','r','n',1,1,'holder','cap','runner','runner','remote',1)`)
	rewriteSchemaExec(t, db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a','digest',1,1)")
	rewriteSchemaExec(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,unit_kind,unit_id,unit_generation,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','owner','r','owner_unit','plan','plan-unit',1,'digest','machine','cap',1)`)
	rewriteSchemaExec(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','publish-binary','r','publication','digest','account','cap',1)`)
	rewriteSchemaExec(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,node_id,attempt,generation,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','publish-output','r','publication','n',1,1,'digest','account','cap',1)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','job-missing-identity','r','job','digest','machine','cap',1)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,node_id,attempt,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','publication-partial','r','publication','n',1,'digest','account','cap',1)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,node_id,attempt,generation,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','foreign-job','r','job','other',1,1,'digest','machine','cap',1)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO blob_receipts(team,id,run_id,uploader_kind,unit_kind,unit_id,unit_generation,digest,uploader_principal,capability_prefix,created_at)
VALUES ('a','duplicate-owner','r','owner_unit','plan','plan-unit',1,'digest','machine','cap',1)`)
}
