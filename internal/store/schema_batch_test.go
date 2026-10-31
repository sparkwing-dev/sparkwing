package store_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestSchemaRejectsIncompleteFinancialDecisions(t *testing.T) {
	s := storetest.Open(t)
	for _, query := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`,
		`INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_at) VALUES ('a','grant','free',1,'reference',1)`,
		`INSERT INTO payment_events(payment_mode,event_id,event_kind,event_digest,object_id,event_json,settlement_json,processed_at) VALUES ('test','event','warning','digest','warning','{}','{}',1)`,
		`INSERT INTO standing_requests(id,team,tier,message,requested_by,requested_at,state) VALUES ('request','a','High','message','owner',1,'open')`,
		`INSERT INTO team_deletions(slug,requested_at,state) VALUES ('deleted',1,'pending')`,
		`INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES ('a','award','prefix','run',1,'a',0,'runner','at_start','node','job',1)`,
	} {
		if _, err := s.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatalf("seed financial rows: %v", err)
		}
	}
	for _, tc := range []struct{ name, query string }{
		{"grant without team", `UPDATE payment_events SET grant_id='grant'`},
		{"unknown grant without team", `UPDATE payment_events SET grant_id='missing'`},
		{"open request with decider", `UPDATE standing_requests SET decided_by='owner'`},
		{"open request with decision time", `UPDATE standing_requests SET decided_at=2`},
		{"approved without decision", `UPDATE standing_requests SET state='approved'`},
		{"approved without decider", `UPDATE standing_requests SET state='approved',decided_at=2`},
		{"declined without decision time", `UPDATE standing_requests SET state='declined',decided_by='owner'`},
		{"deletion done without finish", `UPDATE team_deletions SET state='done'`},
		{"deletion pending with finish", `UPDATE team_deletions SET finished_at=2`},
		{"negative award generation", `UPDATE billing_awards SET generation=-4`},
		{"zero award generation", `UPDATE billing_awards SET generation=0`},
		{"empty grant reference", `UPDATE credit_grants SET reference=''`},
		{"omitted grant reference", `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES ('a','missing-reference','free',1,1)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.DB().ExecContext(t.Context(), tc.query); err == nil || !strings.Contains(strings.ToLower(err.Error()), "constraint") {
				t.Fatalf("invalid financial row: %v", err)
			}
		})
	}
	for _, query := range []string{
		`UPDATE payment_events SET team='a',grant_id='grant'`,
		`UPDATE standing_requests SET state='approved',decided_by='owner',decided_at=2`,
		`UPDATE standing_requests SET state='declined'`,
		`UPDATE standing_requests SET state='open',decided_by='',decided_at=0`,
		`UPDATE team_deletions SET state='done',finished_at=2`,
	} {
		if _, err := s.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatalf("valid financial decision refused: %v", err)
		}
	}
}

func TestSchemaAllowsOnlyOneOpenBillingIntervalPerAward(t *testing.T) {
	s := storetest.Open(t)
	for _, query := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`,
		`INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES ('a','award','prefix','run',1,'a',0,'runner','at_start','node','job',1),('b','award','prefix','run',1,'b',0,'runner','at_start','node','job',1),('a','other','other-prefix','other-run',1,'a',0,'runner','at_start','node','job',1)`,
		`INSERT INTO billable_intervals(team,id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through) SELECT team,team||reservation_id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,1,1 FROM billing_awards`,
	} {
		if _, err := s.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatalf("seed independent open intervals: %v", err)
		}
	}
	insert := `INSERT INTO billable_intervals(team,id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through) SELECT team,'next',run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,2,2 FROM billing_awards WHERE team='a' AND reservation_id='award'`
	if _, err := s.DB().ExecContext(t.Context(), insert); err == nil || !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("second open interval: %v", err)
	}
	if _, err := s.DB().ExecContext(t.Context(), `UPDATE billable_intervals SET finished_at=2 WHERE team='a' AND reservation_id='award'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(), insert); err != nil {
		t.Fatalf("successive interval refused after closure: %v", err)
	}
}
