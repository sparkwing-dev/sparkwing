package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestStoreDataLedgerTotalsUseFinancialRows(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES
	 ('free','a','free',100,1),('paid','b','paid',1000,1),('reversal','b','reversal',-25,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES
	 ('reserve','a','pruned','job','award','reservation',60,600,1),
	 ('refund','a','pruned','job','award','refund',-57,-570,1),
	 ('usage','b','pruned','job','other','usage',7,70,1),
	 ('storage','b','','','','storage',86400,20,1)`)
	want := CreditLedgerTotals{BalanceMicro: 955, GrantedFreeMicro: 100, GrantedPaidMicro: 1000, ReversedMicro: 25, ReservedMicro: 600, ChargedMicro: 70, RefundedMicro: 570, StorageMicro: 20, SettledSeconds: 10}
	for range 2 {
		got, err := s.CreditLedgerTotals(t.Context())
		if err != nil || got != want {
			t.Fatalf("financial totals=%+v, %v; want %+v", got, err, want)
		}
	}
}

func TestStoreDataLedgerTotalsExcludeUnstartedReservation(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	reserveMetering(t, s, c)
	calculate := func(p capability.BillingProvenance, intervals []capability.BillableInterval) (capability.BillingSettlement, error) {
		if p != *c.Billing || len(intervals) != 0 {
			t.Fatalf("unstarted inputs=%+v, %+v", p, intervals)
		}
		return capability.BillingSettlement{ReservationID: p.ReservationID, RefundUnstarted: true}, nil
	}
	got, err := s.creditLedgerTotalsWith(t.Context(), calculate)
	if err != nil || got.SettledSeconds != 0 || got.ReservedMicro != 600 || got.BalanceMicro != 999400 {
		t.Fatalf("unstarted totals=%+v, %v", got, err)
	}
}

func TestStoreDataLedgerTotalsReadAwardsWithoutAdvancingBilling(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	base := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return base }
	iteration := 0
	property := func(suffix string, unitIndex, reserve, elapsed uint8, started, atAward, reopen bool) bool {
		iteration++
		var wantSeconds, wantReserved int64
		type expectation struct {
			provenance capability.BillingProvenance
			seconds    int64
			intervals  []capability.BillableInterval
		}
		want := make(map[string]expectation)
		for owner := range 2 {
			team := Team(fmt.Sprintf("totals_%d_%d_%s_%%'--", iteration, owner, suffix))
			rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", team)
			rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES (?,?,'paid',1000000,1)", string(team), team)
			unit := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode, capability.UnitTrigger}[(int(unitIndex)+owner)%4]
			billing := []capability.BillingUnit{capability.BillingPlan, capability.BillingEval, capability.BillingJob, capability.BillingHost}[(int(unitIndex)+owner)%4]
			c := authorityRoot("same-award", string(team))
			c.Unit = unit
			c.Binding = capability.Binding{RunID: "pruned-run", NodeID: "same-job", Generation: 1}
			start := capability.BillingAtStart
			if atAward {
				start = capability.BillingAtAward
			}
			c.Billing = &capability.BillingProvenance{PayerTeam: string(team), Metered: true, ExecutorClass: "cloud", ReservationID: "same-reservation", Start: start, Unit: billing}
			s.clock = func() time.Time { return base }
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				return recordBillingAwardTx(t.Context(), tx, c, CreditRate{Cores: 2, MicroPerSecond: 10}, int64(reserve)+int64(owner))
			}); err != nil {
				t.Fatal(err)
			}
			compute := time.Duration(int64(elapsed)+1) * time.Second
			s.clock = func() time.Time { return base.Add(compute + time.Hour) }
			expected := expectation{provenance: *c.Billing}
			if started || atAward {
				key := "award"
				if !atAward {
					key = "execute"
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						return startBillableIntervalTx(t.Context(), tx, team, c.Billing.ReservationID, key, 0, base)
					}); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					return finishBillableIntervalTx(t.Context(), tx, team, c.Billing.ReservationID, key, base.Add(compute))
				}); err != nil {
					t.Fatal(err)
				}
				expected.intervals = []capability.BillableInterval{{CapabilityPrefix: c.Prefix, Unit: billing, Start: base, End: base.Add(compute)}}
				expected.seconds = int64(compute / time.Second)
				if billing == capability.BillingJob || billing == capability.BillingHost {
					expected.seconds = max(expected.seconds, 60)
				}
			}
			want[string(team)] = expected
			wantSeconds += min(expected.seconds, int64(reserve)+int64(owner))
			wantReserved += (int64(reserve) + int64(owner)) * 10
		}
		if reopen {
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = blobAccountingDB(t, path)
		}
		calls := 0
		calculate := func(p capability.BillingProvenance, intervals []capability.BillableInterval) (capability.BillingSettlement, error) {
			calls++
			expected, ok := want[p.PayerTeam]
			if !ok || expected.provenance != p || !slices.Equal(expected.intervals, intervals) {
				t.Fatalf("calculation inputs=%+v, %+v; want %+v", p, intervals, expected)
			}
			return capability.BillingSettlement{ReservationID: p.ReservationID, BillableSeconds: expected.seconds, RefundUnstarted: len(intervals) == 0}, nil
		}
		before := billingIntervalCheckpoints(t, s)
		wantTotals := CreditLedgerTotals{GrantedPaidMicro: int64(iteration) * 2_000_000, ReservedMicro: wantReserved, SettledSeconds: wantSeconds, BalanceMicro: int64(iteration)*2_000_000 - wantReserved}
		for range 2 {
			got, err := s.creditLedgerTotalsWith(t.Context(), calculate)
			if err != nil || got != wantTotals {
				t.Logf("totals=%+v, %v; want %+v", got, err, wantTotals)
				return false
			}
		}
		if calls != 4 || !slices.Equal(before, billingIntervalCheckpoints(t, s)) {
			t.Fatal("report advanced billing or skipped an award")
		}
		rewriteSchemaExec(t, s.db, "DELETE FROM billable_intervals")
		rewriteSchemaExec(t, s.db, "DELETE FROM credit_charges")
		rewriteSchemaExec(t, s.db, "DELETE FROM billing_awards")
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(52971))}); err != nil {
		t.Fatal(err)
	}
}

func billingIntervalCheckpoints(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "SELECT team,id,charged_through,finished_at FROM billable_intervals ORDER BY team,id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var out []string
	for rows.Next() {
		var team, id string
		var through int64
		var finished sql.NullInt64
		if err := rows.Scan(&team, &id, &through, &finished); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%q:%q:%d:%v", team, id, through, finished))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStoreDataLedgerTotalsRefuseFailedCalculation(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtAward)
	reserveMetering(t, s, c)
	before := billingIntervalCheckpoints(t, s)
	boom := errors.New("calculator unavailable")
	for _, test := range []struct {
		result capability.BillingSettlement
		err    error
	}{
		{err: boom},
		{result: capability.BillingSettlement{ReservationID: "other"}},
		{result: capability.BillingSettlement{ReservationID: "reservation", BillableSeconds: -1}},
		{result: capability.BillingSettlement{ReservationID: "reservation", RefundUnstarted: true}},
	} {
		got, err := s.creditLedgerTotalsWith(t.Context(), func(capability.BillingProvenance, []capability.BillableInterval) (capability.BillingSettlement, error) {
			return test.result, test.err
		})
		if err == nil || got != (CreditLedgerTotals{}) || (test.err != nil && !errors.Is(err, test.err)) {
			t.Fatalf("invalid calculation=%+v returned %+v, %v", test, got, err)
		}
		if !slices.Equal(before, billingIntervalCheckpoints(t, s)) {
			t.Fatal("failed report advanced billing")
		}
	}
}

func TestStoreDataLedgerTotalsObserveOnlyConfirmedCompute(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitPlan, capability.BillingAtAward)
	reserveMetering(t, s, c)
	at := s.now()
	confirmed := int64(0)
	calculate := func(p capability.BillingProvenance, intervals []capability.BillableInterval) (capability.BillingSettlement, error) {
		want := []capability.BillableInterval{{CapabilityPrefix: c.Prefix, Unit: capability.BillingPlan, Start: at, End: at.Add(time.Duration(confirmed) * time.Second)}}
		if p != *c.Billing || !slices.Equal(intervals, want) {
			t.Fatalf("checkpoint inputs=%+v, %+v; want %+v", p, intervals, want)
		}
		return capability.BillingSettlement{ReservationID: p.ReservationID, BillableSeconds: confirmed}, nil
	}
	for _, step := range []struct {
		now   int64
		renew bool
		want  int64
	}{{3, false, 0}, {3, true, 3}, {70, false, 3}, {70, true, 70}, {3600, false, 70}} {
		s.clock = func() time.Time { return at.Add(time.Duration(step.now) * time.Second) }
		if step.renew {
			confirmed = step.now
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", s.now(), false, calculate)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		before := billingIntervalCheckpoints(t, s)
		got, err := s.creditLedgerTotalsWith(t.Context(), calculate)
		if err != nil || got.SettledSeconds != step.want || got.ReservedMicro != 600 || got.ChargedMicro != max(step.want-60, 0)*10 {
			t.Fatalf("confirmed totals=%+v, %v; step=%+v", got, err, step)
		}
		if !slices.Equal(before, billingIntervalCheckpoints(t, s)) {
			t.Fatal("report charged or closed open compute")
		}
	}
	confirmed = 75
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := settleBillingAwardWithTx(t.Context(), tx, "a", "reservation", at.Add(75*time.Second), calculate)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CreditLedgerTotals(t.Context())
	if err != nil || got.SettledSeconds != confirmed || got.ChargedMicro != 150 {
		t.Fatalf("disrupted totals=%+v, %v", got, err)
	}
}

func TestStoreDataLedgerTotalsSkipUnmeteredAwards(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtAward)
	c.Billing.Metered = false
	reserveMetering(t, s, c)
	if got, err := s.CreditLedgerTotals(t.Context()); err != nil || got != (CreditLedgerTotals{GrantedPaidMicro: 1000000, BalanceMicro: 1000000}) {
		t.Fatalf("unmetered totals=%+v, %v", got, err)
	}
}
