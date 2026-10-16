package store_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestAddNodeUsageRejectsOverflowAtomically(t *testing.T) {
	for _, test := range []struct {
		name      string
		initial   store.NodeUsage
		increment store.NodeUsage
	}{
		{"CPU", store.NodeUsage{CPUTime: time.Duration(math.MaxInt64), Wall: time.Second, MaxRSSBytes: 4096}, store.NodeUsage{CPUTime: 1, Wall: time.Second, MaxRSSBytes: 8192}},
		{"wall", store.NodeUsage{CPUTime: time.Second, Wall: time.Duration(math.MaxInt64), MaxRSSBytes: 4096}, store.NodeUsage{CPUTime: time.Second, Wall: 1, MaxRSSBytes: 8192}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
			a := metricAuthorization(t, f, "usage")
			ctx := t.Context()
			if err := f.tenant.WriteUsage(ctx, a, test.initial); err != nil {
				t.Fatal(err)
			}
			if err := f.tenant.WriteUsage(ctx, a, test.increment); err == nil {
				t.Error("accepted overflowing node usage")
			}
			node := metricUsage(t, f)
			if node.CPUTime != test.initial.CPUTime || node.Wall != test.initial.Wall || node.MaxRSSBytes != test.initial.MaxRSSBytes {
				t.Fatalf("rejected overflow changed totals: CPU=%d wall=%d RSS=%d", node.CPUTime, node.Wall, node.MaxRSSBytes)
			}
		})
	}
}

func TestAddNodeUsageAcceptsExactIntegerBoundary(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
	a := metricAuthorization(t, f, "usage")
	ctx := t.Context()
	if err := f.tenant.WriteUsage(ctx, a, store.NodeUsage{CPUTime: time.Duration(math.MaxInt64 - 1), Wall: time.Duration(math.MaxInt64 - 1), MaxRSSBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	if err := f.tenant.WriteUsage(ctx, a, store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 8192}); err != nil {
		t.Fatal(err)
	}
	node := metricUsage(t, f)
	if node.CPUTime != math.MaxInt64 || node.Wall != math.MaxInt64 || node.MaxRSSBytes != 8192 {
		t.Fatalf("exact boundary changed: CPU=%d wall=%d RSS=%d", node.CPUTime, node.Wall, node.MaxRSSBytes)
	}
}

func TestAddNodeUsageConcurrentWritersRespectBoundary(t *testing.T) {
	target := storetest.New(t)
	f := newStoreRecoveryFixtureAt(t, true, target)
	a := metricAuthorization(t, f, "usage")
	first := f.tenant
	second := tenantFor(t, target.Open(t), f.tenant.Team())
	ctx := t.Context()
	if err := first.WriteUsage(ctx, a, store.NodeUsage{CPUTime: time.Duration(math.MaxInt64 - 1), Wall: time.Duration(math.MaxInt64 - 1)}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, writer := range []*store.Tenant{first, second} {
		go func() {
			<-start
			results <- writer.WriteUsage(ctx, a, store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 8192})
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "overflow") {
			t.Errorf("losing writer: %v, want overflow", err)
		}
	}
	if successes != 1 {
		t.Errorf("successful additions=%d, want 1", successes)
	}
	node := metricUsage(t, f)
	if node.CPUTime != math.MaxInt64 || node.Wall != math.MaxInt64 || node.MaxRSSBytes != 8192 {
		t.Fatalf("concurrent boundary totals: CPU=%d wall=%d RSS=%d", node.CPUTime, node.Wall, node.MaxRSSBytes)
	}
}

func TestAddNodeUsageRejectsNegativeStoredTotals(t *testing.T) {
	for _, column := range []string{"cpu_nanos", "process_wall_nanos"} {
		t.Run(column, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
			a := metricAuthorization(t, f, "usage")
			ctx := t.Context()
			if _, err := f.s.DB().ExecContext(ctx, "UPDATE nodes SET "+column+" = -1"); err != nil {
				t.Fatal(err)
			}
			before := metricUsage(t, f)
			if err := f.tenant.WriteUsage(ctx, a, store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 8192}); err == nil {
				t.Error("accepted negative stored total")
			}
			after := metricUsage(t, f)
			if after.CPUTime != before.CPUTime || after.Wall != before.Wall || after.MaxRSSBytes != before.MaxRSSBytes {
				t.Fatal("rejected observation changed stored usage")
			}
		})
	}
}

func metricUsage(t *testing.T, f *storeRecoveryFixture) store.NodeUsage {
	t.Helper()
	var u store.NodeUsage
	if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, `SELECT cpu_nanos,process_wall_nanos,max_rss_bytes FROM nodes WHERE team=? AND run_id=? AND node_id=?`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID).Scan(&u.CPUTime, &u.Wall, &u.MaxRSSBytes); err != nil {
		t.Fatal(err)
	}
	return u
}

var _ interface {
	WriteMetric(context.Context, capability.Authorized, store.MetricSample) error
	WriteUsage(context.Context, capability.Authorized, store.NodeUsage) error
} = (*store.Tenant)(nil)
