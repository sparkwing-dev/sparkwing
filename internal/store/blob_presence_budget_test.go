package store

import (
	"errors"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestBlobPresenceBudgetNeverReturnsAnExpiredSuccess(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	base := s.now()
	property := func(ticks, copies uint8) bool {
		deadline := base.Add(time.Duration(1+int(ticks)%40) * time.Nanosecond)
		var observed time.Time
		next := base
		s.clock = func() time.Time {
			observed = next
			next = next.Add(time.Nanosecond)
			return observed
		}
		digests := make([]blob.Digest, 1+int(copies)%8)
		for i := range digests {
			digests[i] = ticket.Blob.Digest
		}
		presence, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: request.Intent.Authority, Digests: digests, Budget: blob.Budget{Deadline: deadline}})
		if errors.Is(err, blob.ErrBudget) {
			return len(presence.Present) == 0
		}
		return err == nil && observed.Before(deadline) && len(presence.Present) == 1 && presence.Present[0] == ticket.Blob.Digest
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 160, Rand: rand.New(rand.NewSource(19))}); err != nil {
		t.Fatal(err)
	}
}
