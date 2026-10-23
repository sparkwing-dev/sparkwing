package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestConsoleRefundReceiptSurvivesAuditRetentionSQLite(t *testing.T) {
	assertConsoleRefundReceiptSurvivesAuditRetention(t, storetest.NewSQLite(t))
}

func TestConsoleRefundReceiptSurvivesAuditRetentionPostgres(t *testing.T) {
	assertConsoleRefundReceiptSurvivesAuditRetention(t, storetest.NewPostgres(t))
}

func assertConsoleRefundReceiptSurvivesAuditRetention(t *testing.T, target *storetest.Target) {
	t.Helper()
	s := target.Open(t)
	operator := signIn(t, s, "operator", "operator@example.test")
	team := tenant(t, s, operator.Account.ActiveTeam)
	actor := membershipActor(t, s, operator.Account.ID, team.Team())
	a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialConsole, Authority: actor.Authority, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectAccount, ID: operator.Account.ID}}}, Target: capability.Target{Team: string(team.Team())}}
	allowed := func(id string) bool { return id == operator.Account.ID }
	console := s.AsConsole(allowed)
	if _, err := team.TestOnlySettlePaidCredit(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "payment"}); err != nil {
		t.Fatal(err)
	}
	req := store.OperatorRefund{PaymentID: "payment", RefundID: "original-refund", AmountCents: 1, Reason: "support"}
	original, err := console.ConsoleRefund(t.Context(), a, req, "original")
	if err != nil || !original.Created || original.Grant == nil {
		t.Fatalf("original receipt: %+v: %v", original, err)
	}
	alreadyRecorded, err := console.ConsoleRefund(t.Context(), a, req, "already-recorded")
	if err != nil || alreadyRecorded.Created || alreadyRecorded.Grant == nil {
		t.Fatalf("receipt for existing refund: %+v: %v", alreadyRecorded, err)
	}
	unrelated := req
	unrelated.RefundID, unrelated.AmountCents = "later-refund", 2
	if _, err := console.ConsoleRefund(t.Context(), a, unrelated, "later"); err != nil {
		t.Fatal(err)
	}
	ageAuditEvents(t, s, "operator.refund_recorded", time.Now().Add(-401*24*time.Hour))
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "refund-retention", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := s.AsOperator().MaintainRetention(t.Context(), store.RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || pruned.AuditEvents != 3 {
		t.Fatalf("refund audit pruning: %+v: %v", pruned, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = target.Open(t)
	console = s.AsConsole(allowed)
	for key, want := range map[string]store.CreditReversal{"original": original, "already-recorded": alreadyRecorded} {
		got, err := console.ConsoleRefund(t.Context(), a, req, key)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s receipt after pruning and restart: %+v; want %+v: %v", key, got, want, err)
		}
	}
	changed := req
	changed.RefundID = "changed-refund"
	_, err = console.ConsoleRefund(t.Context(), a, changed, "original")
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.Conflict {
		t.Fatalf("changed request after audit retention: %v", err)
	}
	if n := countWhere(t, s, "credit_grants", "kind='reversal'"); n != 2 {
		t.Fatalf("replay changed refund ledger: %d reversals", n)
	}
	if n := countWhere(t, s, "audit_events", "kind='operator.refund_recorded'"); n != 0 {
		t.Fatalf("replay recreated expired audit: %d", n)
	}
}
