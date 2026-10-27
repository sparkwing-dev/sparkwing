package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type stagingDeleteFunc func(context.Context, string) error

func (f stagingDeleteFunc) DeleteObject(ctx context.Context, key string) error { return f(ctx, key) }

func TestBlobStagingCleanupRetriesUntilDeleted(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	request.ExpiresAt = now.Add(time.Minute)
	committed, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(committed, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	abandoned := request
	abandoned.ID, abandoned.Intent.Digest = "ticket_b", blob.Digest(strings.Repeat("b", 64))
	expired, err := tenant.ReserveBlobUpload(t.Context(), abandoned)
	if err != nil {
		t.Fatal(err)
	}
	live := request
	live.ID, live.Intent.Digest, live.ExpiresAt = "ticket_c", blob.Digest(strings.Repeat("c", 64)), now.Add(4*limits.BlobGCGrace)
	if _, err := tenant.ReserveBlobUpload(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	var deleted []string
	fail := errors.New("object store unavailable")
	failing := map[string]bool{expired.StagingKey: true}
	deleter := stagingDeleteFunc(func(_ context.Context, key string) error {
		deleted = append(deleted, key)
		if failing[key] {
			return fail
		}
		return nil
	})
	sweep := func() error { return s.AsOperator().DeleteExpiredBlobStaging(t.Context(), 10, deleter) }
	now = request.ExpiresAt
	if _, err := s.AsOperator().MarkBlobTombstones(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if err := sweep(); err != nil || len(deleted) != 0 {
		t.Fatalf("staging deleted inside grace: %v, %v", deleted, err)
	}
	now = now.Add(limits.BlobGCGrace)
	if err := sweep(); !errors.Is(err, fail) || !slices.Equal(deleted, []string{committed.StagingKey, expired.StagingKey}) {
		t.Fatalf("first sweep = %v, deleted %v", err, deleted)
	}
	delete(failing, expired.StagingKey)
	deleted = nil
	if err := sweep(); err != nil || !slices.Equal(deleted, []string{expired.StagingKey}) {
		t.Fatalf("retry sweep = %v, deleted %v", err, deleted)
	}
	deleted = nil
	if err := sweep(); err != nil || len(deleted) != 0 {
		t.Fatalf("settled sweep = %v, deleted %v", err, deleted)
	}
}
