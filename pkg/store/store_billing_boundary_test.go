package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestStoreBillingRetention(t *testing.T) {
	s, tenant, now := seedStoreBillingBoundary(t)
	op := s.AsOperator()
	rate, free := int64(StorageBytesPerGB), int64(3)
	if _, err := op.SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free}); err != nil {
		t.Fatal(err)
	}
	lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "billing-boundary", 200*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if initial, err := op.ChargeRetainedStorage(t.Context(), lease); err != nil || len(initial.Charges) != 0 {
		t.Fatalf("initial sample: %+v: %v", initial, err)
	}
	*now = now.Add(24 * time.Hour)
	billed, err := op.ChargeRetainedStorage(t.Context(), lease)
	if err != nil || len(billed.Charges) != 1 {
		t.Fatalf("storage charge: %+v: %v", billed, err)
	}
	charge := billed.Charges[0]
	if charge.Team != tenant.Team() || charge.StorageBytes != StorageBytesPerGB || charge.RunID != "" || charge.Seconds != StorageChargeDaySeconds || charge.AmountMicro != rate {
		t.Fatalf("storage oracle: %+v", charge)
	}
	before, err := billingFinancialSnapshot(t.Context(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	state, err := tenant.CreditState(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := tenant.ListCreditGrants(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	charges, err := tenant.ListCreditCharges(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := tenant.CreditUsageByRun(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	totals, err := op.CreditLedgerTotals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(120 * 24 * time.Hour)
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)", []byte(`90`))
	result, err := op.MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || result.Runs != 1 {
		t.Fatalf("retention: %+v: %v", result, err)
	}
	for _, table := range []string{"runs", "log_streams", "log_segments"} {
		rewriteSchemaCount(t, s.db, table, 0)
	}
	blobAccountingDiskStore(t, s)
	after, err := billingFinancialSnapshot(t.Context(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("retention or reopening changed financial evidence")
	}
	afterState, err := tenant.CreditState(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	afterGrants, err := tenant.ListCreditGrants(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	afterCharges, err := tenant.ListCreditCharges(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	afterUsage, err := tenant.CreditUsageByRun(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	afterTotals, err := op.CreditLedgerTotals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, afterState) || !reflect.DeepEqual(grants, afterGrants) || !reflect.DeepEqual(charges, afterCharges) || !slices.Equal(usage, afterUsage) || totals != afterTotals {
		t.Fatal("retention or reopening changed billing reports")
	}
}

func seedStoreBillingBoundary(t *testing.T) (*Store, *Tenant, *time.Time) {
	t.Helper()
	s, demand, now := meteredUnitFixture(t, false)
	s.billing = nil
	tenant := &Tenant{s: s, team: "a"}
	op := s.AsOperator()
	rewriteSchemaExec(t, s.db, "DELETE FROM credit_grants")
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"test"`))
	if err := op.CreateTeam(t.Context(), "neighbor_%'--"); err != nil {
		t.Fatal(err)
	}
	for _, team := range []Team{"a", "neighbor_%'--"} {
		payer := &Tenant{s: s, team: team}
		if _, _, err := payer.SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "console", Reason: "fixture"}, *now); err != nil {
			t.Fatal(err)
		}
		checkout, err := payer.OpenCreditCheckout(t.Context(), 10000*MicroCreditsPerCent, *now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		session := "session-" + string(team)
		if err := payer.AttachCreditCheckout(t.Context(), checkout, session, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.settlePaymentFacts(t.Context(), capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "paid-" + string(team), CheckoutID: session, PaymentID: "payment-" + string(team), Kind: capability.PaymentCheckoutPaid, Currency: "usd", AmountMinor: 10000}); err != nil {
			t.Fatal(err)
		}
		if _, err := payer.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: 1000000, Reference: "caller-selected", CreatedBy: "billing"}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("raw paid grant: %v", err)
		}
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "customer", PaymentMethod: "method", Fingerprint: "card"}, "provider", *now); err != nil {
		t.Fatal(err)
	}
	table := CreditRateTable{{Cores: 1, MicroPerSecond: 1000000}, {Cores: 4, MicroPerSecond: 1000000}}
	if _, err := op.SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
		t.Fatal(err)
	}
	claim, actor := meteredUnitClaim(t, s, demand)
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{claim.RunID, claim.NodeID}, Actor: actor, Cause: CauseStart, To: NodeRunning, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	attempt := runspec.AttemptIdentity{Team: "a", RunID: claim.RunID, NodeID: claim.NodeID, Attempt: 1, Generation: claim.Award.Generation}
	if err := tenant.RecordSegments(t.Context(), logCommit(actor, attempt)); err != nil {
		t.Fatal(err)
	}
	intent := blobIntent(actor, attempt)
	intent.Authority.Principal, intent.Identity.Principal = blob.Principal(actor.Holder), blob.Principal(actor.Holder)
	intent.Size = StorageBytesPerGB
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(3 * time.Second)
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{claim.RunID, claim.NodeID}, Actor: actor, Cause: CauseReport, To: NodeFailed, SourceAttempt: sourceAttempt(1)}); err != nil {
		t.Fatal(err)
	}
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: claim.RunID}
	ownerClaim, owner := meteredUnitClaim(t, s, demand)
	if _, err := tenant.FinishRunAndUnit(t.Context(), FinishRunRequest{Transition: RunTransition{RunID: claim.RunID, To: RunFailed, Actor: owner, Cause: RunFinish}, Unit: owner.Owner}); err != nil {
		t.Fatalf("finish %s: %v", ownerClaim.RunID, err)
	}
	if _, err := op.TestOnlyReversePayment(t.Context(), "payment-a", "refund", "provider", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := op.DueCardCharges(t.Context(), *now); err != nil {
		t.Fatal(err)
	}
	*now = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	workItems, _, err := op.DueCardCharges(t.Context(), *now)
	if err != nil || len(workItems) != 1 {
		t.Fatalf("collection work: %+v: %v", workItems, err)
	}
	work := workItems[0]
	payment := CardPayment{Team: "a", ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "recovery", AmountCents: work.AmountCents}
	if _, _, err := op.SettleCardPayment(t.Context(), payment, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return queueCardRefundTx(t.Context(), tx, tenant.team, payment, "fixture", *now)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := op.TestOnlyRecordPaymentWarning(t.Context(), PaymentWarning{WarningID: "warning", PaymentIntent: payment.PaymentIntent}, *now); err != nil {
		t.Fatal(err)
	}
	hold, err := tenant.HoldByOperator(t.Context(), "console", "fixture", *now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ReleaseCreditFreezes(t.Context(), hold, "console", "resolved", *now); err != nil {
		t.Fatal(err)
	}
	completion, err := op.AcquireSingletonLease(t.Context(), "completion", "billing-boundary", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := op.DueRunEffects(t.Context(), completion, 100)
	if err != nil || len(effects) != 4 {
		t.Fatalf("completion work: %+v: %v", effects, err)
	}
	for _, effect := range effects {
		if err := tenant.AcknowledgeRunEffect(t.Context(), EffectDelivery{RunID: effect.RunID, Effect: effect.Effect, Revision: effect.DesiredRev, Lease: completion}); err != nil {
			t.Fatal(err)
		}
	}
	return s, tenant, now
}

func billingFinancialSnapshot(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	tables := append(FinancialTables(), "payment_events")
	snapshot := make(map[string][]string, len(tables))
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			snapshot[table] = append(snapshot[table], string(encoded))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if len(snapshot[table]) == 0 {
			return nil, fmt.Errorf("missing financial fixture for %s", table)
		}
		slices.Sort(snapshot[table])
	}
	return snapshot, nil
}
