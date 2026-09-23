package controller

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type measuredStore struct {
	usage storage.StoreUsage
	err   error
	calls int
}

func (m *measuredStore) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (m *measuredStore) Put(context.Context, string, io.Reader) error       { return nil }
func (m *measuredStore) Has(context.Context, string) (bool, error)          { return false, nil }
func (m *measuredStore) Delete(context.Context, string) error               { return nil }
func (m *measuredStore) List(context.Context, string) ([]string, error)     { return nil, nil }

func (m *measuredStore) Usage(context.Context) (storage.StoreUsage, error) {
	m.calls++
	return m.usage, m.err
}

type unmeasuredStore struct{ measuredStore }

func (u *unmeasuredStore) Usage(context.Context) (storage.StoreUsage, error) {
	panic("the unmeasurable store was asked for a total")
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

// A controller pointed at a bucket measures it whether or not a ceiling is
// set, so the totals it reports are the bucket's, never zeros.
func TestTheBucketIsMeasuredWithNoCeilingSet(t *testing.T) {
	ceiling := objectguard.NewCeiling(objectguard.CeilingConfig{})
	store := &measuredStore{usage: storage.StoreUsage{Bytes: 69 << 20, Objects: 412, ObservedAt: time.Now()}}
	s := New(nil, nil).WithBucketUsage(store)
	if ran, err := s.measureBucketLeased(context.Background(), ceiling, 0); err != nil || !ran {
		t.Fatalf("measure = %t, %v", ran, err)
	}
	state := ceiling.State()
	if state.Bytes != 69<<20 || state.Objects != 412 || state.ReconciledAt.IsZero() || state.Incomplete {
		t.Fatalf("an unlimited bucket's state = %+v, want the measured 69 MiB in 412 objects", state)
	}
	if state.Enforced || state.Frozen {
		t.Fatalf("measuring an unlimited bucket enforced or froze it: %+v", state)
	}
	summary, problems := objectStoreHealth(true)
	if _, ok := summary["ceiling"]; !ok || len(problems) != 0 {
		t.Fatalf("health with a measured bucket = %v %v, want a ceiling summary and no problem", summary, problems)
	}
}

// A measurement that fails says so in the ceiling's state and in health,
// rather than leaving the last totals to read as current.
func TestAFailedBucketMeasurementReachesHealth(t *testing.T) {
	limiter, err := objectguard.Shared()
	if err != nil {
		t.Fatal(err)
	}
	ceiling := limiter.Ceiling()
	saved := ceiling.State()
	t.Cleanup(func() {
		ceiling.Configure(objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{
			MaxBytes: saved.MaxBytes, MaxObjects: saved.MaxObjects, WarnBytes: saved.WarnBytes, WarnObjects: saved.WarnObjects,
		}})
		ceiling.Observe(objectguard.Usage{})
	})
	ceiling.Configure(objectguard.CeilingConfig{})
	s := New(nil, nil).WithBucketUsage(&measuredStore{err: errors.New("AccessDenied: list bucket")})
	s.runBucketCeiling(context.Background())
	state := ceiling.State()
	if !state.Incomplete || !strings.Contains(state.MeasureError, "AccessDenied") {
		t.Fatalf("state after a failed measurement = %+v, want it incomplete and naming the error", state)
	}
	summary, problems := objectStoreHealth(true)
	bucket, _ := summary["ceiling"].(map[string]any)
	if bucket["measurement_incomplete"] != true || len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "measure") {
		t.Fatalf("health after a failed measurement = %v %v, want it incomplete with a problem", summary, problems)
	}
	if summary, problems := objectStoreHealth(false); summary["ceiling"] != nil || len(problems) != 0 {
		t.Fatalf("health with no bucket = %v %v", summary, problems)
	}
}
