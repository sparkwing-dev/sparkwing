package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
)

type measuredStore struct {
	usage storage.StoreUsage
	err   error
	calls int
}

func (m *measuredStore) Usage(context.Context) (storage.StoreUsage, error) {
	m.calls++
	return m.usage, m.err
}

func TestBucketUsageMeasuresTheStoreTheCeilingWasPointedAt(t *testing.T) {
	store := &measuredStore{usage: storage.StoreUsage{Bytes: 4096, Objects: 12, ObservedAt: time.Now()}}
	s := New(nil, nil).WithBucketUsage(store)

	usage, err := s.bucketUsage(context.Background())
	if err != nil || usage.Partial {
		t.Fatalf("bucketUsage: partial=%t err=%v", usage.Partial, err)
	}
	if usage.Bytes != 4096 || usage.Objects != 12 {
		t.Errorf("measured %d bytes / %d objects, want 4096/12", usage.Bytes, usage.Objects)
	}
	if store.calls != 1 {
		t.Errorf("the store was measured %d times for one reconciliation, want 1", store.calls)
	}
}

func TestBucketUsageReportsNothingWithoutAStore(t *testing.T) {
	usage, err := New(nil, nil).bucketUsage(context.Background())
	if err != nil {
		t.Fatalf("bucketUsage: %v", err)
	}
	if !usage.Partial {
		t.Error("a controller with no object store reported a complete measurement")
	}
}

func TestBucketUsagePassesAPartialMeasurementThrough(t *testing.T) {
	s := New(nil, nil).WithBucketUsage(&measuredStore{
		usage: storage.StoreUsage{Bytes: 10, Objects: 1, Partial: true},
	})
	usage, err := s.bucketUsage(context.Background())
	if err != nil {
		t.Fatalf("bucketUsage: %v", err)
	}
	if !usage.Partial {
		t.Error("a store that stopped measuring early reported a complete total")
	}
}

func TestBucketUsageSurfacesAFailedMeasurement(t *testing.T) {
	want := errors.New("bucket unreachable")
	s := New(nil, nil).WithBucketUsage(&measuredStore{err: want})

	if _, err := s.bucketUsage(context.Background()); !errors.Is(err, want) {
		t.Fatalf("bucketUsage error = %v, want it to wrap %v", err, want)
	}
}

// Measuring a bucket with no ceiling records its totals without enforcing
// or freezing anything.
func TestMeasuringAnUnlimitedBucketNeitherEnforcesNorFreezes(t *testing.T) {
	ceiling := objectguard.NewCeiling(objectguard.CeilingConfig{})
	store := &measuredStore{usage: storage.StoreUsage{Bytes: 69 << 20, Objects: 412, ObservedAt: time.Now()}}
	s := New(nil, nil).WithBucketUsage(store)
	if err := s.measureBucket(context.Background(), ceiling, 0); err != nil {
		t.Fatalf("measure: %v", err)
	}
	state := ceiling.State()
	if state.Bytes != 69<<20 || state.Objects != 412 || state.ReconciledAt.IsZero() || state.Incomplete {
		t.Fatalf("an unlimited bucket's state = %+v, want the measured 69 MiB in 412 objects", state)
	}
	if state.Enforced || state.Frozen || state.Measured {
		t.Fatalf("measuring an unlimited bucket enforced, froze or published it: %+v", state)
	}
}

// A measurement that fails says so in the ceiling's state and in health,
// rather than leaving the last totals to read as current.
func TestAFailedBucketMeasurementReachesHealth(t *testing.T) {
	limiter := objectguard.Shared()
	ceiling := limiter.Ceiling()
	saved := ceiling.State()
	t.Cleanup(func() {
		ceiling.Configure(objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{
			MaxBytes: saved.MaxBytes, MaxObjects: saved.MaxObjects, WarnBytes: saved.WarnBytes, WarnObjects: saved.WarnObjects,
		}})
		ceiling.Observe(objectguard.Usage{})
	})
	ceiling.Configure(objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{WarnBytes: 1 << 40}})
	s := New(nil, nil).WithBucketUsage(&measuredStore{err: errors.New("AccessDenied: list bucket")})
	s.runBucketCeiling(context.Background())
	state := ceiling.State()
	if !state.Incomplete || !strings.Contains(state.MeasureError, "AccessDenied") {
		t.Fatalf("state after a failed measurement = %+v, want it incomplete and naming the error", state)
	}
	summary, problems := objectStoreHealth()
	bucket, _ := summary["ceiling"].(map[string]any)
	if bucket["measurement_incomplete"] != true || len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "measure") {
		t.Fatalf("health after a failed measurement = %v %v, want it incomplete with a problem", summary, problems)
	}
	ceiling.Configure(objectguard.CeilingConfig{})
	if summary, problems := objectStoreHealth(); summary["ceiling"] != nil || len(problems) != 0 {
		t.Fatalf("health with no ceiling = %v %v, want no unmeasured totals", summary, problems)
	}
}

// With no ceiling and no warning mark nothing reads the totals, so the
// controller never lists the bucket, even when pointed at one.
func TestAnUnboundedCeilingNeverListsTheBucket(t *testing.T) {
	ceiling := objectguard.Shared().Ceiling()
	saved := ceiling.State()
	t.Cleanup(func() {
		ceiling.Configure(objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{
			MaxBytes: saved.MaxBytes, MaxObjects: saved.MaxObjects, WarnBytes: saved.WarnBytes, WarnObjects: saved.WarnObjects,
		}})
	})
	ceiling.Configure(objectguard.CeilingConfig{Reconcile: time.Millisecond})
	store := &measuredStore{usage: storage.StoreUsage{Bytes: 1, Objects: 1, ObservedAt: time.Now()}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	New(nil, nil).WithBucketUsage(store).runBucketCeiling(ctx)
	if store.calls != 0 {
		t.Fatalf("an unbounded ceiling listed the bucket %d times", store.calls)
	}
}
