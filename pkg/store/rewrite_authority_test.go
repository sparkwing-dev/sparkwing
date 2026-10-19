package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRewriteSchemaCapabilitiesPreserveAuthority(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	rewriteSchemaExec(t, db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,
repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES ('a','r',1,'p','pending',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{"version":1}`), []byte(`[]`))
	rewriteSchemaExec(t, db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team) VALUES ('session','account','runs.write',1,100,'a')`)
	deadline := time.Unix(100, 7).UTC()
	cases := []struct {
		name     string
		parent   capability.AuthorityKind
		unit     capability.UnitKind
		purpose  capability.AwardPurpose
		delivery capability.DeliveryPurpose
		billing  capability.BillingUnit
	}{
		{"here", capability.AuthoritySession, capability.UnitTrigger, capability.PurposeHere, capability.DeliveryDirect, capability.BillingHost},
		{"work", capability.AuthorityCapability, capability.UnitNode, capability.PurposeWork, capability.DeliveryDirect, capability.BillingJob},
		{"plan_bootstrap", capability.AuthorityCapability, capability.UnitPlan, capability.PurposePlan, capability.DeliveryBootstrap, capability.BillingPlan},
		{"eval", capability.AuthorityCapability, capability.UnitEval, capability.PurposeEval, capability.DeliveryDirect, capability.BillingEval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := CapabilityRecord{Version: 1, Value: capability.Capability{
				Prefix: tc.name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "machine"},
				Audience: capability.Controller, Grants: []capability.Grant{capability.DataRead}, Issuance: capability.Award,
				Unit: tc.unit, UnitID: "unit-" + tc.name, Purpose: tc.purpose, Delivery: tc.delivery,
				Parent:        &capability.AuthorityRef{Kind: tc.parent, ID: "session"},
				ReservedPools: []string{"prod"}, Repositories: []string{"gh:1"}, RootMetered: true,
				IssuedAt: time.Unix(1, 2).UTC(), ExpiresAt: &deadline,
				Binding: capability.Binding{
					Kind: capability.BindingNode, RunID: "r", NodeID: "job", Generation: 2, Holder: "holder", Attempt: 3,
					ClaimBy: &deadline, Deadline: &deadline, Reservation: "here:r",
					Push:      &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: "sha", WorkflowRunID: "workflow"},
					Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: "eval", Generation: 4, Holder: "evaluator"},
				},
				Data: &capability.DataScope{
					Use: capability.DataExpansion, RepositoryKey: "gh:1", WriteRef: "manual:main",
					ReadRefs: []string{"main", "release"}, VerifiedDefaultOnly: true, RunID: "r", NodeID: "job", Attempt: 3, Generation: 2,
				},
				Billing: &capability.BillingProvenance{PayerTeam: "a", Metered: true, ExecutorClass: "cloud", ReservationID: "res-" + tc.name, Start: capability.BillingAtStart, Unit: tc.billing},
			}}
			encoded, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			digest := "digest-" + tc.name
			rewriteSchemaExec(t, db, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,
authority_version,authority_json,parent_kind,parent_id,award_purpose,delivery_purpose,unit_id,billing_unit,
bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,payer_team,executor_class,reservation_id,billing_start,unit_kind)
VALUES (?,?,'a','machine','machine','controller',?,'award',1,?,?,?,?,?,?,?,'node','r','job',2,'holder',?,1,'account','a','cloud',?,'at_start',?)`,
				digest, tc.name, []byte(`["data.read"]`), encoded, tc.parent, "session", tc.purpose, tc.delivery, want.Value.UnitID, tc.billing, []byte(`["prod"]`), want.Value.Billing.ReservationID, tc.unit)
			var stored []byte
			var gotDigest, parentKind, parentID, purpose, delivery, unitID, billing string
			if err := db.QueryRow(`SELECT digest,authority_json,parent_kind,parent_id,award_purpose,delivery_purpose,unit_id,billing_unit FROM capabilities WHERE prefix = ?`, tc.name).
				Scan(&gotDigest, &stored, &parentKind, &parentID, &purpose, &delivery, &unitID, &billing); err != nil {
				t.Fatal(err)
			}
			var got CapabilityRecord
			if err := json.Unmarshal(stored, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("authority changed across persistence: got %#v, want %#v", got, want)
			}
			if gotDigest != digest || parentKind != string(tc.parent) || parentID != "session" || purpose != string(tc.purpose) || delivery != string(tc.delivery) || unitID != want.Value.UnitID || billing != string(tc.billing) {
				t.Fatal("indexed authority projections changed across persistence")
			}
		})
	}
	rewriteSchemaRefuses(t, db, `UPDATE capabilities SET parent_kind = 'unknown' WHERE prefix = 'here'`)
	rewriteSchemaRefuses(t, db, `UPDATE capabilities SET parent_id = NULL WHERE prefix = 'here'`)
	rewriteSchemaRefuses(t, db, `UPDATE capabilities SET unit_kind = 'here' WHERE prefix = 'here'`)
	rewriteSchemaRefuses(t, db, `UPDATE capabilities SET unit_kind = 'plan' WHERE prefix = 'work'`)
	rewriteSchemaRefuses(t, db, `UPDATE capabilities SET billing_unit = 'job' WHERE prefix = 'here'`)
	rewriteSchemaRefuses(t, db, `UPDATE capabilities SET delivery_purpose = 'bootstrap' WHERE prefix = 'here'`)
}

func TestRewriteSchemaBillingClassifications(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	for _, mapping := range capability.UnitBillingMappings() {
		key := string(mapping.Operational)
		rewriteSchemaExec(t, db, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,reserved_amount_micro)
VALUES ('a',?,?,'retained-run',1,'a',1,'cloud','at_start',?,?,1,10)`, key, key, mapping.Operational, mapping.Billing)
		rewriteSchemaExec(t, db, `INSERT INTO billable_intervals(team,id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through)
VALUES ('a',?,'retained-run',1,?,?,'a',1,'cloud',?,?,'at_start',1,2)`, key, key, key, mapping.Operational, mapping.Billing)
		for _, table := range []string{"billing_awards", "billable_intervals"} {
			var got string
			if err := db.QueryRow("SELECT billing_unit FROM "+table+" WHERE reservation_id = ?", key).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != string(mapping.Billing) {
				t.Fatalf("%s %s: got billing %q", table, key, got)
			}
			rewriteSchemaRefuses(t, db, "UPDATE "+table+" SET billing_unit = 'invalid' WHERE reservation_id = ?", key)
		}
	}
	rewriteSchemaRefuses(t, db, "UPDATE billing_awards SET billing_unit = 'job' WHERE unit_kind = 'trigger'")
	rewriteSchemaRefuses(t, db, "UPDATE billable_intervals SET billing_unit = 'host' WHERE unit_kind = 'node'")
}

func TestRewriteSchemaPaymentEventsKeepCheckoutTeamAndDeduplicate(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	rewriteSchemaExec(t, db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at)
VALUES ('checkout','a','provider-session','test','usd',100,1000000,1,2)`)
	rewriteSchemaExec(t, db, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,event_kind,event_digest,processed_at)
VALUES ('a','test','event','checkout','checkout_expired','digest',2)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,event_kind,event_digest,processed_at)
VALUES ('a','test','event','checkout','checkout_expired','digest',2)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,event_kind,event_digest,processed_at)
VALUES ('b','test','foreign-event','checkout','checkout_expired','digest',2)`)
	rewriteSchemaRefuses(t, db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at)
VALUES ('other','b','provider-session','test','usd',100,1000000,1,2)`)
	rewriteSchemaRefuses(t, db, "UPDATE payment_events SET payment_mode = 'live' WHERE event_id = 'event'")
	rewriteSchemaCount(t, db, "payment_events", 1)
}

func TestRewriteSchemaIntakeKeysIncludePublishedRuns(t *testing.T) {
	db := rewriteSchemaDB(t)
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	const insert = `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,
repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,
idempotency_scope,idempotency_key,idempotency_digest)
VALUES (?,?,?,'p','pending',?,'request',1,'gh:1','host',?,?,'account',1,'r30',30,?,?,?)`
	args := func(team, id string, number int, mode, scope, key, digest string) []any {
		return []any{team, id, number, []byte(`{"version":1}`), mode, []byte(`[]`), scope, key, digest}
	}
	rewriteSchemaExec(t, db, insert, args("a", "published", 1, "publish", "account/p", "key", "digest")...)
	rewriteSchemaRefuses(t, db, insert, args("a", "same-key", 2, "publish", "account/p", "key", "digest")...)
	rewriteSchemaRefuses(t, db, insert, args("a", "changed-body", 3, "execute", "account/p", "key", "other")...)
	rewriteSchemaExec(t, db, insert, args("a", "other-principal", 4, "execute", "other/p", "key", "digest")...)
	rewriteSchemaExec(t, db, insert, args("b", "other-team", 1, "publish", "account/p", "key", "digest")...)
	rewriteSchemaExec(t, db, insert, args("a", "unkeyed-one", 5, "publish", "", "", "")...)
	rewriteSchemaExec(t, db, insert, args("a", "unkeyed-two", 6, "publish", "", "", "")...)
	rewriteSchemaRefuses(t, db, insert, args("a", "missing-digest", 7, "publish", "account/p", "another", "")...)
	rewriteSchemaCount(t, db, "runs", 5)
}
