package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// A refund Stripe failed is mailed to the team's owners once, naming its amount and the payment's date.
func TestAFailedRefundIsMailedToTheOwnersOnce(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	paid := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC).UnixNano()
	engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('owner','owner@example.test',1,1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
	engineSQL(t, e.Store, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,payment_id,currency,amount_minor,amount_micro,opened_at,expires_at,paid_at,closed_at,liability_shift)
	    VALUES ('failed-checkout','a','cs_failed','test','pi_failed','usd',1234,?,1,2,NULL,?,0),('pending-checkout','a','cs_pending','test','pi_pending','usd',500,?,1,2,NULL,?,0)`, 1234*store.MicroCreditsPerCent, paid, 500*store.MicroCreditsPerCent, paid)
	engineSQL(t, e.Store, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,amount_micro,refund_id,status,created_at)
	    VALUES ('test','a','pi_failed','refused',?,'re_failed','failed',?),('test','a','pi_pending','refused',?,'re_pending','pending',?)`,
		1234*store.MicroCreditsPerCent, paid, 500*store.MicroCreditsPerCent, paid)
	mail := &refundMail{}
	worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithMailer(mail)
	for range 2 {
		worker.noticeFailedRefunds(t.Context())
	}
	if len(mail.sent) != 1 || mail.sent[0].To != "owner@example.test" || !strings.Contains(mail.sent[0].Text, "$12.34") ||
		!strings.Contains(mail.sent[0].Text, "October 1, 2026") || !strings.Contains(mail.sent[0].Text, "Sparkwing will follow up") {
		t.Fatalf("owner mail %+v; want one failed-refund notice", mail.sent)
	}
}

type refundMail struct{ sent []mailer.Message }

func (m *refundMail) Send(_ context.Context, msg mailer.Message) error {
	m.sent = append(m.sent, msg)
	return nil
}
