package store

import (
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestConsoleRefundCurrentAuthorityPayerAndRetry(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	owner := authorityMember(t, s, "operator", RoleReader)
	c := s.AsConsole(func(id string) bool { return id == "operator" })
	a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialConsole, Authority: owner.Authority, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}}}, Target: capability.Target{Team: "a"}}
	req := controllerwire.OperatorRefund{PaymentID: event.PaymentID, RefundID: "re_console", AmountCents: 1, Reason: "support"}
	property := func(noise string, dimension uint8) bool {
		changed := a
		switch dimension % 4 {
		case 0:
			changed.Target.Team = "foreign-" + noise
		case 1:
			changed.Principal.Capability.Subject.ID = "foreign-" + noise
		case 2:
			changed.Principal.Class = capability.CredentialBearer
		case 3:
			changed.Principal.Authority.ID = "foreign-" + noise
		}
		got, err := c.ConsoleRefund(t.Context(), changed, req, "request")
		return err != nil && !got.Created && got.Grant == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
	foreign := a
	foreign.Target.Team = "b"
	if got, err := c.ConsoleRefund(t.Context(), foreign, req, "request"); err == nil || got.Grant != nil {
		t.Fatalf("foreign payer: %+v %v", got, err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_console_refund BEFORE INSERT ON audit_events WHEN NEW.kind='operator.refund_recorded' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if got, err := c.ConsoleRefund(t.Context(), a, req, "request"); err == nil || got.Grant != nil {
		t.Fatalf("audit failure returned refund: %+v %v", got, err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
	rewriteSchemaCount(t, s.db, "payment_refunds", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_console_refund`)
	got, err := c.ConsoleRefund(t.Context(), a, req, "request")
	if err != nil || !got.Created || got.Team != "a" || got.Grant == nil || got.Grant.AmountMicro != -MicroCreditsPerCent || got.Grant.CreatedBy != "operator" {
		t.Fatalf("refund = %+v: %v", got, err)
	}
	replay, err := c.ConsoleRefund(t.Context(), a, req, "request")
	if err != nil || replay.Created || replay.Grant == nil || replay.Grant.ID != got.Grant.ID {
		t.Fatalf("replay = %+v: %v", replay, err)
	}
	req.AmountCents++
	if _, err := c.ConsoleRefund(t.Context(), a, req, "request"); err == nil {
		t.Fatal("changed replay succeeded")
	}
	provider := capability.PaymentEvent{Mode: event.Mode, EventID: "refund-delivery", Kind: capability.PaymentRefund, PaymentID: event.PaymentID, ObjectID: req.RefundID, ChargeID: "ch_refund", Currency: "usd", AmountMinor: 1, Status: "pending", Created: 1000}
	if _, err := s.settlePaymentFacts(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	var refundStatus string
	if err := s.db.QueryRow(`SELECT status FROM payment_refunds WHERE payment_mode=? AND kind='refund' AND refund_id=?`, event.Mode, req.RefundID).Scan(&refundStatus); err != nil || refundStatus != CardRefundSucceeded {
		t.Fatalf("console response regressed after pending callback: %s, %v", refundStatus, err)
	}
	provider.EventID, provider.Status = "refund-terminal-delivery", CardRefundSucceeded
	if _, err := s.settlePaymentFacts(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 2)
	tenant := &Tenant{s: s, team: "a"}
	state, err := tenant.CreditState(t.Context(), 0)
	if err != nil || state.GrantedMicro != 1_000_000 || state.ReversedMicro != MicroCreditsPerCent || state.BalanceMicro != 1_000_000-MicroCreditsPerCent {
		t.Fatalf("refund ledger: %+v, %v", state, err)
	}
	totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
	if err != nil || totals.ReversedMicro != MicroCreditsPerCent || totals.BalanceMicro != state.BalanceMicro {
		t.Fatalf("refund totals: %+v, %v", totals, err)
	}
	grants, err := tenant.ListCreditGrants(t.Context(), 10)
	if err != nil || len(grants) != 2 {
		t.Fatalf("refund history: %+v, %v", grants, err)
	}
	for _, grant := range grants {
		if grant.Kind == CreditGrantReversal && (grant.AmountMicro != -MicroCreditsPerCent || grant.Reverses != "payment:test:checkout") {
			t.Fatalf("refund lost its payment reference: %+v", grant)
		}
	}
	events, err := (&Tenant{s: s, team: "a"}).BusinessEvents(t.Context(), "operator.refund_recorded")
	if err != nil || len(events) != 1 || events[0].Actor != "operator" || events[0].Attrs["reason"] != "support" {
		t.Fatalf("audit = %+v: %v", events, err)
	}
	req.AmountCents--
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash='operator'`)
	if _, err := c.ConsoleRefund(t.Context(), a, req, "request"); err == nil {
		t.Fatal("revoked replay succeeded")
	}
}
