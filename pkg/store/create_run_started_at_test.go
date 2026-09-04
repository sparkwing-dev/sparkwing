package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A run created without a start time has to land on the timeline the
// lists, filters and reapers all read, not before it.
func TestCreateRun_ZeroStartedAtStartsNow(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	before := time.Now()

	if err := s.CreateRun(ctx, store.Run{ID: "z", Pipeline: "demo", Status: "pending"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := s.GetRun(ctx, "z")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.StartedAt.Before(before) || got.StartedAt.After(time.Now()) {
		t.Fatalf("StartedAt = %s, want between %s and now", got.StartedAt, before)
	}

	recent, err := s.ListRuns(ctx, store.RunFilter{Since: before.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != "z" {
		t.Fatalf("ListRuns(since) = %v runs, want the one just created", len(recent))
	}
}

// Rows written before the zero start time was substituted read back as
// an unset time rather than a 1754 date, the way created_at already does.
func TestScanRun_UndefinedStartedAtReadsBackUnset(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	if _, err := s.DB().Exec(
		`INSERT INTO runs (id, pipeline, status, created_at, started_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"legacy", "demo", "pending", time.Time{}.UnixNano(), time.Time{}.UnixNano(),
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	got, err := s.GetRun(ctx, "legacy")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !got.StartedAt.IsZero() {
		t.Fatalf("StartedAt = %s, want the zero time", got.StartedAt)
	}
	if !got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt = %s, want the zero time", got.CreatedAt)
	}
}
