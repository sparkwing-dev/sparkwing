package capability

import (
	"testing"
	"time"
)

func TestBillingSettlement(t *testing.T) {
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		unit    BillingUnit
		elapsed time.Duration
		want    int64
	}{
		{"plan", BillingPlan, 7 * time.Second, 7},
		{"eval", BillingEval, 11 * time.Second, 11},
		{"short job", BillingJob, 7 * time.Second, 60},
		{"minimum job", BillingJob, 60 * time.Second, 60},
		{"long job", BillingJob, 61 * time.Second, 61},
		{"fractional plan", BillingPlan, 1500 * time.Millisecond, 2},
		{"instant job", BillingJob, 0, 60},
		{"host minimum", BillingHost, time.Second, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provenance := BillingProvenance{PayerTeam: "team", Metered: true, ExecutorClass: "cloud", ReservationID: "reservation", Start: BillingAtStart, Unit: tc.unit}
			intervals := []BillableInterval{{CapabilityPrefix: "award", Start: start, End: start.Add(tc.elapsed), Unit: tc.unit}}
			got, err := SettleBilling(provenance, intervals)
			if err != nil || got.BillableSeconds != tc.want || got.RefundUnstarted || got.ReservationID != "reservation" {
				t.Fatalf("settlement=%+v err=%v", got, err)
			}
			got, err = SettleBilling(provenance, nil)
			if err != nil || got.BillableSeconds != 0 || !got.RefundUnstarted {
				t.Fatalf("unstarted=%+v err=%v", got, err)
			}
			provenance.Metered = false
			got, err = SettleBilling(provenance, intervals)
			if err != nil || got.BillableSeconds != 0 || got.RefundUnstarted {
				t.Fatalf("unmetered=%+v err=%v", got, err)
			}
		})
	}
	provenance := BillingProvenance{PayerTeam: "team", Metered: true, ExecutorClass: "cloud", ReservationID: "reservation", Start: BillingAtAward, Unit: BillingHost}
	intervals := []BillableInterval{
		{CapabilityPrefix: "award", Start: start, End: start.Add(70 * time.Second), Unit: BillingHost},
		{CapabilityPrefix: "award", Start: start.Add(670 * time.Second), End: start.Add(740 * time.Second), Unit: BillingHost},
	}
	got, err := SettleBilling(provenance, intervals)
	if err != nil || got.BillableSeconds != 140 {
		t.Fatalf("wait exclusion=%+v err=%v", got, err)
	}
	for _, tc := range []struct {
		name   string
		change func([]BillableInterval)
	}{
		{"overlap", func(x []BillableInterval) { x[1].Start = start.Add(time.Second) }},
		{"reversed", func(x []BillableInterval) { x[0].End = start.Add(-time.Second) }},
		{"host as job", func(x []BillableInterval) { x[1].Unit = BillingJob }},
		{"no actor", func(x []BillableInterval) { x[0].CapabilityPrefix = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := append([]BillableInterval(nil), intervals...)
			tc.change(x)
			if _, err := SettleBilling(provenance, x); err == nil {
				t.Fatal("invalid intervals accepted")
			}
		})
	}
}
