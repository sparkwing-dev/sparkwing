package store

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func billingModelFixture(t *testing.T) (*Store, UnitDemand, *time.Time) {
	t.Helper()
	s, demand, now := meteredUnitFixture(t, true)
	rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET kind='free'`)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=?`, now.Add(24*time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='card',card_added_at=? WHERE name='a'`, now.UnixNano())
	return s, demand, now
}

func billingModelJob(t *testing.T, s *Store, id string) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`, id)
}

func TestBillingModelNewCloudAdmissionsShareEightVCPU(t *testing.T) {
	s, demand, _ := billingModelFixture(t)
	for _, id := range []string{"job", "second", "third"} {
		if id != "job" {
			billingModelJob(t, s, id)
		}
		demand.Target.NodeID = id
		claim, err := s.ClaimUnit(t.Context(), demand)
		if id != "third" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil || len(claim.Bearer) > 0 {
			t.Fatalf("New exceeded 8 concurrent Cloud vCPU: %+v, %v", claim, err)
		}
		if !strings.Contains(err.Error(), "New") {
			t.Fatalf("refusal omits standing: %v", err)
		}
	}
}

func TestBillingModelNewCloudJobHasHardFourHourDeadline(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	claim, _ := meteredUnitClaim(t, s, demand)
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(4 * time.Hour)
	if p.Capability.Binding.Deadline == nil || !p.Capability.Binding.Deadline.Equal(want) {
		t.Fatalf("New job deadline=%v; want %v", p.Capability.Binding.Deadline, want)
	}
	*now = want.Add(-time.Second)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=? WHERE team='a' AND run_id='run' AND node_id='job'`, want.Add(time.Minute).UnixNano())
	beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
	if err != nil || !beat.ExpiresAt.Equal(want) {
		t.Fatalf("renewal escaped hard deadline: %+v: %v", beat, err)
	}
	*now = want
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err == nil {
		t.Fatal("job renewed past hard deadline")
	}
}

func TestBillingModelPrepayImmediatelyLiftsCloudStanding(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	tenant := &Tenant{s: s, team: "a"}
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	checkout, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), 5000*MicroCreditsPerCent, *now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.TestOnlyAttachCreditCheckout(t.Context(), checkout, "prepay-session", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settlePaymentFacts(t.Context(), capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "prepay", CheckoutID: "prepay-session", PaymentID: "prepay-payment", Kind: capability.PaymentCheckoutPaid, AmountMinor: 5000, Currency: "usd"}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"job", "second", "third"} {
		if id != "job" {
			billingModelJob(t, s, id)
		}
		demand.Target.NodeID = id
		if _, err := s.ClaimUnit(t.Context(), demand); err != nil {
			t.Fatalf("prepay did not lift standing: %v", err)
		}
	}
	standing, err := (&Tenant{s: s, team: "a"}).BillingStanding(t.Context(), s.now())
	if err != nil || standing.Tier != "Established" {
		t.Fatalf("prepaid standing=%+v: %v", standing, err)
	}
}

func TestBillingModelDisputeOutcomePreservesStandingProperty(t *testing.T) {
	property := func(seed uint32) bool {
		s, tenant, now := providerPaymentFixture(t)
		providerCheckoutPaid(t, s)
		rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust=''`)
		statuses := []string{"lost", "won", "warning_closed"}
		status := statuses[seed%uint32(len(statuses))]
		events := []capability.PaymentEventKind{capability.PaymentDisputeOpened, capability.PaymentDisputeClosed, capability.PaymentDisputeOpened, capability.PaymentDisputeClosed}
		rand.New(rand.NewSource(int64(seed))).Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
		closed := false
		for i, kind := range events {
			event := providerPaymentEvent(now, kind)
			event.ObjectID, event.EventID, event.Status = "dispute", fmt.Sprintf("dispute-event-%d", i), "needs_response"
			if kind == capability.PaymentDisputeClosed {
				event.Status, closed = status, true
			}
			for range 2 {
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			standing, err := tenant.BillingStanding(t.Context(), now)
			if err != nil || (standing.Tier == "Held") != (!closed || status == "lost") {
				t.Logf("outcome=%s closed=%t standing=%+v: %v", status, closed, standing, err)
				return false
			}
		}
		balance, err := tenant.CreditBalanceMicro(t.Context())
		wantBalance := int64(100 * MicroCreditsPerCent)
		if status == "lost" {
			wantBalance = 0
			if released, err := tenant.ReleaseCreditFreezes(t.Context(), "dispute:test:dispute", "operator", "reviewed", now); err != nil || released != 1 {
				t.Fatalf("operator release=%d: %v", released, err)
			}
		}
		return err == nil && balance == wantBalance
	}
	for seed := range uint32(3) {
		if !property(seed) {
			t.Fatalf("dispute changed standing: seed=%d", seed)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24, Rand: rand.New(rand.NewSource(1122))}); err != nil {
		t.Fatal(err)
	}
}

func TestBillingModelCloudAdmissionCapacityProperty(t *testing.T) {
	property := func(seed uint32) bool {
		s, demand, _ := billingModelFixture(t)
		rates := CreditRateTable{{Cores: 1, MicroPerSecond: 1}, {Cores: 2, MicroPerSecond: 2}, {Cores: 4, MicroPerSecond: 4}, {Cores: 8, MicroPerSecond: 8}, {Cores: 16, MicroPerSecond: 16}}
		if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
			t.Fatal(err)
		}
		demand.Available.Cores, demand.Capacity.Cores = 64, 64
		allocated := 0
		for i := range 9 {
			cores := 1 << ((seed >> uint(i*3%27)) % 5)
			id := fmt.Sprintf("generated-%d", i)
			billingModelJob(t, s, id)
			execution := fmt.Sprintf(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":%d}}}}`, cores)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id=?`, []byte(execution), id)
			demand.Target.NodeID = id
			claim, err := s.ClaimUnit(t.Context(), demand)
			if allocated+cores <= 8 {
				if err != nil {
					t.Logf("fitting job refused: %v", err)
					return false
				}
				allocated += cores
			} else if err == nil || len(claim.Bearer) != 0 || !strings.Contains(err.Error(), "New") {
				t.Logf("capacity bypass: allocated=%d requested=%d: %+v: %v", allocated, cores, claim, err)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(112))}); err != nil {
		t.Fatal(err)
	}
}

func TestBillingModelTrustedNeedsPaidHistoryAndCleanAge(t *testing.T) {
	s, _, now := billingModelFixture(t)
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.TestOnlySettlePaidCredit(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: 5000 * MicroCreditsPerCent, Reference: "payment", CreatedBy: "billing"}); err != nil {
		t.Fatal(err)
	}
	standing, err := tenant.BillingStanding(t.Context(), *now)
	if err != nil || standing.Tier != "Established" {
		t.Fatalf("young history: %+v: %v", standing, err)
	}
	*now = now.Add(30 * 24 * time.Hour)
	standing, err = tenant.BillingStanding(t.Context(), *now)
	if err != nil || standing.Tier != "Trusted" || standing.CloudVCPU != 128 || standing.MaxJobDuration != 6*time.Hour {
		t.Fatalf("clean paid history: %+v: %v", standing, err)
	}
}

func TestBillingModelIndependentClaimantsCannotOversubscribe(t *testing.T) {
	s, demand, _ := billingModelFixture(t)
	billingModelJob(t, s, "second")
	billingModelJob(t, s, "third")
	path := blobAccountingDiskStore(t, s)
	clients := []*Store{s, {db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}, {db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}}
	start, results := make(chan struct{}), make(chan error, 3)
	for i, id := range []string{"job", "second", "third"} {
		request := demand
		target := *demand.Target
		target.NodeID = id
		request.Target = &target
		go func(client *Store) { <-start; _, err := client.ClaimUnit(t.Context(), request); results <- err }(clients[i])
	}
	close(start)
	admitted := 0
	for range 3 {
		if err := <-results; err == nil {
			admitted++
		} else if !strings.Contains(err.Error(), "New") {
			t.Fatal(err)
		}
	}
	if admitted != 2 {
		t.Fatalf("admitted %d four-core jobs; want 2", admitted)
	}
}

func TestBillingModelCustomerRunnerHasNoStandingCapacityOrDeadline(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, false)
	deadline := now.Add(24 * time.Hour)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=?`, deadline.UnixNano())
	for _, id := range []string{"job", "second", "third"} {
		if id != "job" {
			billingModelJob(t, s, id)
		}
		demand.Target.NodeID = id
		claim, _ := meteredUnitClaim(t, s, demand)
		principal, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
		if err != nil || principal.Capability.Binding.Deadline == nil || !principal.Capability.Binding.Deadline.Equal(deadline) {
			t.Fatalf("customer runner inherited standing deadline: %+v: %v", principal.Capability.Binding.Deadline, err)
		}
	}
}

func TestBillingModelCloudReclaimCannotRestartJobDeadline(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	began := *now
	claim, _ := meteredUnitClaim(t, s, demand)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(3 * time.Hour)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=1 WHERE team='a' AND run_id='run' AND node_id='job'`)
	replacement, _ := meteredUnitClaim(t, s, demand)
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: replacement.Award.CapabilityPrefix})
	if err != nil || p.Capability.Binding.Deadline == nil || !p.Capability.Binding.Deadline.Equal(began.Add(4*time.Hour)) {
		t.Fatalf("reclaim restarted job deadline: %+v: %v", p.Capability.Binding.Deadline, err)
	}
}

// A funding stop persists on the metered job even after the balance or hold recovers.
func TestBillingModelEveryHoldStopsRunningCloudWork(t *testing.T) {
	for _, hold := range []struct{ name, id, cause, says string }{
		{"dispute", "dp_1", HoldCauseDispute, "dispute"},
		{"warning", "efw_1", HoldCauseWarning, "fraud"},
		{"operator", OperatorHoldPrefix + "x", HoldCauseOperator, "support"},
		{"revoked", "", HoldCauseRevoked, "revoked"},
		{"out of credits", "", "unpaid", "add funds"},
		{"no hold", "", "control", ""},
	} {
		t.Run(hold.name, func(t *testing.T) {
			s, demand, now := billingModelFixture(t)
			claim, actor := meteredUnitClaim(t, s, demand)
			tenant := &Tenant{s: s, team: "a"}
			if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			switch {
			case hold.id != "":
				placed := ""
				if hold.cause == HoldCauseOperator {
					placed = "console"
				}
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES (?,'a','r',?,?,?)`, hold.id, hold.cause, placed, now.UnixNano())
			case hold.cause == HoldCauseRevoked:
				rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='revoked' WHERE name='a'`)
			case hold.cause == "unpaid":
				rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=600`)
			}
			*now = now.Add(30 * time.Second)
			beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil {
				t.Fatal(err)
			}
			if stopped := hold.cause != "control"; beat.CancelRequested != stopped || stopped && !strings.Contains(beat.CancelReason, hold.says) {
				t.Fatalf("cancel requested=%v (%s) with %s", beat.CancelRequested, beat.CancelReason, hold.name)
			}
			if hold.cause == "control" {
				return
			}
			if !beat.ExpiresAt.After(*now) || beat.ExpiresAt.After(claim.Award.LeaseExpiresAt) {
				t.Fatalf("lease runs to %v from %v; want a drain within the funded slice", beat.ExpiresAt, *now)
			}
			node, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
			if err != nil || node.CancelRequestedAt == nil || !strings.Contains(node.CancelReason, hold.says) {
				t.Fatalf("the stop left the job uncancelled: %+v %v", node, err)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM credit_freezes`)
			rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='' WHERE name='a'`)
			rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES ('a','late','paid',?,1)`, int64(MaxCreditGrantMicro))
			*now = now.Add(10 * time.Second)
			again, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || !again.CancelRequested || !again.ExpiresAt.Equal(beat.ExpiresAt) {
				t.Fatalf("lifting the hold resumed stopped work: %+v %v", again, err)
			}
			*now = beat.ExpiresAt
			if late, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatalf("a runner that kept renewing past its drain deadline was renewed: %+v", late)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants WHERE id='late'`)
			if hold.cause != "unpaid" {
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES ('dp_2','a','r',?,'',?)`, HoldCauseDispute, now.UnixNano())
			}
			billingModelJob(t, s, "second")
			demand.Target.NodeID = "second"
			if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatalf("%s admitted new Cloud work", hold.name)
			}
		})
	}
}
