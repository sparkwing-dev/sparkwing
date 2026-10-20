package store

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestReaperFenceProperty(t *testing.T) {
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, now, lease := ownerReaperFixture(t, i%2 == 0, 0, runspec.OriginTrigger)
			before, err := tenant.ReadSnapshot(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			for _, forged := range []SingletonLease{
				{Name: "maintenance", Holder: lease.Holder, Generation: lease.Generation},
				{Name: lease.Name, Holder: fmt.Sprintf("foreign-%d", i), Generation: lease.Generation},
				{Name: lease.Name, Holder: lease.Holder, Generation: lease.Generation + int64(i+1)},
			} {
				if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), forged, 10); err == nil || len(reaped) != 0 {
					t.Fatalf("forged fence reaped: %+v, %v", reaped, err)
				}
			}
			*now = lease.ExpiresAt
			if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err == nil || len(reaped) != 0 {
				t.Fatalf("expired fence reaped: %+v, %v", reaped, err)
			}
			after, err := tenant.ReadSnapshot(t.Context(), "run")
			before.StoreNow = after.StoreNow
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused reaper changed persisted snapshot: %v", err)
			}
			next, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), lease.Name, fmt.Sprintf("successor-%d", i), time.Hour)
			if err != nil || next.Generation != lease.Generation+1 {
				t.Fatalf("successor: %+v, %v", next, err)
			}
			if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err == nil {
				t.Fatal("former holder reaped after takeover")
			}
			if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), next, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("current fence: %+v, %v", reaped, err)
			}
		})
	}
}
