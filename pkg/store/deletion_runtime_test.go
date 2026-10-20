package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestTeamPurgeIsolationProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(919, 317))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			teams := []Team{"a", "b", Team(fmt.Sprintf("tenant-'\";--%x", random.Uint64()))}
			if err := s.AsOperator().CreateTeam(t.Context(), teams[2]); err != nil {
				t.Fatal(err)
			}
			root := authorityRoot("deployment", "")
			root.Subject.Kind = capability.SubjectService
			authorityPersist(t, s, root)
			rewriteSchemaExec(t, s.db, `INSERT INTO payment_events(payment_mode,event_id,event_kind,event_digest,processed_at,object_id,event_json,settlement_json) VALUES ('test','unbound','warning','digest',1,'warning','{}','{}')`)
			for j, team := range teams {
				seedPurgeScope(t, s, team, j)
			}
			target := teams[random.IntN(len(teams))]
			before := purgeRows(t, s, target, false)
			if len(purgeRows(t, s, target, true)) == 0 {
				t.Fatal("purge target has no rows")
			}
			for range 2 {
				if err := s.AsOperator().deleteTenantRows(t.Context(), target); err != nil {
					t.Fatal(err)
				}
				if rows := purgeRows(t, s, target, true); len(rows) != 0 {
					t.Fatalf("target rows survived purge: %v", rows)
				}
				if after := purgeRows(t, s, target, false); !slices.Equal(before, after) {
					t.Fatalf("purge changed another owner: before=%v after=%v", before, after)
				}
			}
			if _, err := s.db.Exec("DELETE FROM teams WHERE name = ?", target); err != nil {
				t.Fatalf("purge left a tenant reference: %v", err)
			}
		})
	}
}

func seedPurgeScope(t *testing.T, s *Store, team Team, index int) {
	t.Helper()
	id := fmt.Sprint(index)
	c := authorityRoot("cap-"+id, string(team))
	c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "deployment"}
	authorityPersist(t, s, c)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1)`, id, id+"@example.com")
	rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,?,'',1,2,?,?)`, id, id, team, id)
	rewriteSchemaExec(t, s.db, `INSERT INTO business_events(id,ts,team,kind) VALUES (?,1,?,'test')`, id, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test',?,'setup',?,'customer')`, id, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_events(team,payment_mode,event_id,event_kind,event_digest,processed_at,object_id,event_json,settlement_json) VALUES (?,'test',?,'card_setup','digest',1,?,'{}','{}')`, team, id, id)
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,?,1,'p','pending',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, team, id, []byte(`{}`), []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES (?,'p',?,1)`, team, id)
	rewriteSchemaExec(t, s.db, `INSERT INTO blobs(team,digest,size,created_at) VALUES (?,'manifest',1,1),(?,'member',1,1)`, team, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,'manifest','member')`, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO blob_records(team,kind,scope,name,digest,manifest_digest,run_id,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at) VALUES (?,'output','scope','name','member','manifest',?,'gh:1','main','account','local',?,'publication',?,0,0,?,1)`, team, id, []byte(`{}`), id, []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,prefix,retention_class,created_at,updated_at) VALUES (?,?,'log','owner_unit',1,'plan','prefix','r30',1,1)`, team, id)
	rewriteSchemaExec(t, s.db, `INSERT INTO log_segments(team,run_id,stream_id,generation,n,stored_bytes,object_key,sha256,start_offset,end_offset) VALUES (?,?,'log',1,0,1,'object','digest',0,1)`, team, id)
	rewriteSchemaExec(t, s.db, `INSERT INTO quota_admission(team,quota,period,updated_at) VALUES (?,'quota','period',1)`, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO quota_reservations(team,id,quota,period,amount,expires_at,state) VALUES (?,'hold','quota','period',1,2,'held')`, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES (?,?,'paid',10,1)`, team, "grant-"+id)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(team,id,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES (?,?,?,'job',?,'usage',1,1,1)`, team, "charge-"+id, id, c.Prefix)
	rewriteSchemaExec(t, s.db, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES (?,'award',?,?,1,?,0,'runner','at_start','node','job',1)`, team, c.Prefix, id, team)
	rewriteSchemaExec(t, s.db, `INSERT INTO billable_intervals(team,id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through) VALUES (?,?,?,1,?,'award',?,0,'runner','node','job','at_start',1,1)`, team, id, id, c.Prefix, team)
}

func purgeRows(t *testing.T, s *Store, team Team, target bool) []string {
	t.Helper()
	var out []string
	for _, table := range SchemaTables {
		if table.Name == "teams" {
			continue
		}
		predicate := ""
		var args []any
		if table.Owner == SchemaTenant || slices.Contains([]string{"business_events", "capabilities", "sessions", "payment_events"}, table.Name) {
			predicate, args = " WHERE team IS NOT ?", []any{team}
			if target {
				predicate = " WHERE team = ?"
			}
		} else if target {
			continue
		}
		rows, err := s.db.Query("SELECT * FROM "+table.Name+predicate, args...)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			out = append(out, table.Name+fmt.Sprint(values))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(out)
	return out
}

func TestAccountDeletionRelabelsGrantsAcrossTeamsWithoutChangingBalances(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('leaver','leaver@example.com',1,1)`)
	for i, row := range []struct{ team, creator string }{
		{"a", "leaver"}, {"b", "leaver@example.com"}, {"b", "someone-else"},
	} {
		rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_by,created_at) VALUES (?,?,'paid',10,?,1)`, row.team, fmt.Sprint(i), row.creator)
	}
	if _, err := s.DeleteAccount(t.Context(), "leaver", time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{DeletedUserLabel, DeletedUserLabel, "someone-else"} {
		var creator string
		if err := s.queryRow(t.Context(), `SELECT created_by FROM credit_grants WHERE id=?`, fmt.Sprint(i)).Scan(&creator); err != nil || creator != want {
			t.Fatalf("grant %d creator = %q, %v; want %q", i, creator, err, want)
		}
	}
	for _, tc := range []struct {
		team    Team
		balance int64
	}{{"a", 10}, {"b", 20}} {
		if got, err := (&Tenant{s: s, team: tc.team}).CreditBalanceMicro(t.Context()); err != nil || got != tc.balance {
			t.Fatalf("team %s balance = %d, %v; want %d", tc.team, got, err, tc.balance)
		}
	}
}
