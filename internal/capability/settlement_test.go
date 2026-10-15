package capability_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestBillingSettlement(t *testing.T) {
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		unit    capability.BillingUnit
		elapsed time.Duration
		want    int64
	}{
		{"plan", capability.BillingPlan, 7 * time.Second, 7},
		{"eval", capability.BillingEval, 11 * time.Second, 11},
		{"short job", capability.BillingJob, 7 * time.Second, 60},
		{"minimum job", capability.BillingJob, 60 * time.Second, 60},
		{"long job", capability.BillingJob, 61 * time.Second, 61},
		{"fractional plan", capability.BillingPlan, 1500 * time.Millisecond, 2},
		{"instant job", capability.BillingJob, 0, 60},
		{"host minimum", capability.BillingHost, time.Second, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provenance := capability.BillingProvenance{PayerTeam: "team", Metered: true, ExecutorClass: "cloud", ReservationID: "reservation", Start: capability.BillingAtStart, Unit: tc.unit}
			intervals := []capability.BillableInterval{{CapabilityPrefix: "award", Start: start, End: start.Add(tc.elapsed), Unit: tc.unit}}
			got, err := capability.SettleBilling(provenance, intervals)
			if err != nil || got.BillableSeconds != tc.want || got.RefundUnstarted || got.ReservationID != "reservation" {
				t.Fatalf("settlement=%+v err=%v", got, err)
			}
			got, err = capability.SettleBilling(provenance, nil)
			if err != nil || got.BillableSeconds != 0 || !got.RefundUnstarted {
				t.Fatalf("unstarted=%+v err=%v", got, err)
			}
			provenance.Metered = false
			got, err = capability.SettleBilling(provenance, intervals)
			if err != nil || got.BillableSeconds != 0 || got.RefundUnstarted {
				t.Fatalf("unmetered=%+v err=%v", got, err)
			}
		})
	}
	provenance := capability.BillingProvenance{PayerTeam: "team", Metered: true, ExecutorClass: "cloud", ReservationID: "reservation", Start: capability.BillingAtAward, Unit: capability.BillingHost}
	intervals := []capability.BillableInterval{
		{CapabilityPrefix: "award", Start: start, End: start.Add(70 * time.Second), Unit: capability.BillingHost},
		{CapabilityPrefix: "award", Start: start.Add(670 * time.Second), End: start.Add(740 * time.Second), Unit: capability.BillingHost},
	}
	got, err := capability.SettleBilling(provenance, intervals)
	if err != nil || got.BillableSeconds != 140 {
		t.Fatalf("wait exclusion=%+v err=%v", got, err)
	}
	for _, tc := range []struct {
		name   string
		change func([]capability.BillableInterval)
	}{
		{"overlap", func(x []capability.BillableInterval) { x[1].Start = start.Add(time.Second) }},
		{"reversed", func(x []capability.BillableInterval) { x[0].End = start.Add(-time.Second) }},
		{"host as job", func(x []capability.BillableInterval) { x[1].Unit = capability.BillingJob }},
		{"no actor", func(x []capability.BillableInterval) { x[0].CapabilityPrefix = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := append([]capability.BillableInterval(nil), intervals...)
			tc.change(x)
			if _, err := capability.SettleBilling(provenance, x); err == nil {
				t.Fatal("invalid intervals accepted")
			}
		})
	}
}
