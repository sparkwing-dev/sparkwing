package controller

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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
