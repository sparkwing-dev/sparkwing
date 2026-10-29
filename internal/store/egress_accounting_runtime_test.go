package store

import (
	"math"
	"math/rand"
	"sync"
	"testing"
	"testing/quick"
	"time"
)

func TestStoreDataEgressConservationAndTenantIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 4.5s measured in the short class; the full suite runs it")
	}
	property := func(first, second uint16) bool {
		s, tenant, _ := maintenanceStore(t)
		path := blobAccountingDiskStore(t, s)
		other := &Store{db: blobAccountingDB(t, path), clock: s.clock}
		amounts := []int64{int64(first), int64(second)}
		handles := []*Tenant{tenant, {s: other, team: "a"}}
		var wg sync.WaitGroup
		errors := make(chan error, 2)
		for i, handle := range handles {
			wg.Go(func() {
				errors <- handle.AddUsage(t.Context(), UsageDelta{Period: "2026-10-15", Counter: "egress_bytes", Amount: amounts[i]})
			})
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				t.Log(err)
				return false
			}
		}
		foreign := &Tenant{s: other, team: "b"}
		if err := foreign.AddUsage(t.Context(), UsageDelta{Period: "2026-10-15", Counter: "egress_bytes", Amount: 17}); err != nil {
			t.Fatal(err)
		}
		for _, period := range []string{"2026-10", "2026-10-15"} {
			usage, err := tenant.EgressUsage(t.Context(), period)
			neighbor, foreignErr := foreign.EgressUsage(t.Context(), period)
			if err != nil || foreignErr != nil || usage.Team != "a" || usage.Period != period || usage.Bytes != amounts[0]+amounts[1] || neighbor.Team != "b" || neighbor.Bytes != 17 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(7381))}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataEgressSurvivesRestartAndRetention(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	*now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, period := range []string{"2025-08", "2025-09-01", "2026-10-14", "2026-10-15"} {
		if err := tenant.AddUsage(t.Context(), UsageDelta{Period: period, Counter: "egress_bytes", Amount: 10}); err != nil {
			t.Fatal(err)
		}
	}
	path := blobAccountingDiskStore(t, s)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	if n, err := s.AsOperator().PruneEgressUsage(t.Context()); err != nil || n != 1 {
		t.Fatalf("prune=%d: %v", n, err)
	}
	for period, want := range map[string]int64{"2025-08": 0, "2025-09": 10, "2026-10": 20, "2026-10-14": 10, "2026-10-15": 10, "2026-11": 0} {
		usage, err := tenant.EgressUsage(t.Context(), period)
		if err != nil || usage.Bytes != want {
			t.Fatalf("usage=%+v: %v, want %d", usage, err, want)
		}
	}
	for _, invalid := range []string{"", "2026-1", "2026-13", "2026-02-30", "2026-10%", "2026-10-15' OR 1=1"} {
		if _, err := tenant.EgressUsage(t.Context(), invalid); err == nil {
			t.Fatalf("accepted invalid period %q", invalid)
		}
	}
	rows, err := s.AsOperator().EgressUsageAcrossTeams(t.Context(), "2026-10")
	if err != nil || len(rows) != 1 || rows[0].Team != "a" || rows[0].Bytes != 20 {
		t.Fatalf("operator usage=%+v: %v", rows, err)
	}
}

func TestStoreDataEgressReportsRefuseCorruptionAndOverflow(t *testing.T) {
	s, tenant, _ := maintenanceStore(t)
	for _, row := range []struct {
		team   string
		period string
		amount int64
	}{{"a", "2026-10-15", 7}, {"a", "2026-10-16", 11}, {"b", "2026-10-15", 30}} {
		handle := &Tenant{s: s, team: Team(row.team)}
		if err := handle.AddUsage(t.Context(), UsageDelta{Period: row.period, Counter: "egress_bytes", Amount: row.amount}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.AsOperator().EgressUsageAcrossTeams(t.Context(), "2026-10")
	if err != nil || len(rows) != 2 || rows[0].Team != "b" || rows[0].Bytes != 30 || rows[1].Team != "a" || rows[1].Bytes != 18 || rows[1].UpdatedAt != s.now() {
		t.Fatalf("report=%+v: %v", rows, err)
	}
	rewriteSchemaExec(t, s.db, "PRAGMA ignore_check_constraints=ON")
	for _, amount := range []int64{-1, math.MaxInt64} {
		rewriteSchemaExec(t, s.db, "UPDATE egress_usage SET bytes=? WHERE team='a' AND period='2026-10-15'", amount)
		if _, err := tenant.EgressUsage(t.Context(), "2026-10"); err == nil {
			t.Fatalf("tenant accepted invalid total from %d", amount)
		}
		if _, err := s.AsOperator().EgressUsageAcrossTeams(t.Context(), "2026-10"); err == nil {
			t.Fatalf("operator accepted invalid total from %d", amount)
		}
	}
	if err := tenant.AddUsage(t.Context(), UsageDelta{Period: "2026-10-15", Counter: "egress_bytes", Amount: 1}); err == nil {
		t.Fatal("overflowed a day counter")
	}
	var persisted int64
	if err := s.db.QueryRow("SELECT bytes FROM egress_usage WHERE team='a' AND period='2026-10-15'").Scan(&persisted); err != nil || persisted != math.MaxInt64 {
		t.Fatalf("overflow changed counter=%d: %v", persisted, err)
	}
}
