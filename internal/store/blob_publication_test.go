package store

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobPublicationRequiresTerminalImportedAttempt(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.5s measured in the short class; the full suite runs it")
	}
	s, tenant, logs := publishedLogFixture(t)
	rewriteSchemaExec(t, s.db, "DELETE FROM log_streams")
	authority := dataAccessAuthority(t, s, logs.Actor.CapabilityPrefix)
	iteration := 0
	property := func(ordinal, generation uint16) bool {
		iteration++
		attempt := *logs.Subject.Attempt
		attempt.Attempt, attempt.Generation = int64(ordinal)+1, int64(generation)+1
		rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET attempt=?,generation=?,finished_at=2 WHERE team='a' AND run_id='run'", attempt.Attempt, attempt.Generation)
		rewriteSchemaExec(t, s.db, "UPDATE nodes SET attempt=?,generation=? WHERE team='a' AND run_id='run'", attempt.Attempt, attempt.Generation)
		selected := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}
		input := blob.Authority{CapabilityRef: logs.Actor.CapabilityPrefix, Attempt: selected}
		identity := blob.UploadIdentity{Kind: blob.UploadPublication, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: input.CapabilityRef, Attempt: selected}
		request := UploadReservationRequest{ID: fmt.Sprintf("publication_%d", iteration), Intent: blob.UploadIntent{Authority: input, Identity: identity, Digest: blob.Digest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Size: 3}, ExpiresAt: s.now().Add(time.Hour)}
		ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		var existingBlobs int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&existingBlobs); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=NULL,outcome='' WHERE team='a' AND run_id='run'")
		allowed := false
		for _, check := range []struct {
			name string
			run  func() error
		}{
			{"reserve", func() error { _, err := tenant.ReserveBlobUpload(t.Context(), request); return err }},
			{"lookup", func() error { _, err := tenant.LookupBlobUpload(t.Context(), input, ticket.ID); return err }},
			{"commit", func() error {
				_, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, input))
				return err
			}},
			{"abort", func() error { return tenant.AbortBlobUpload(t.Context(), input, ticket.ID) }},
		} {
			if err := check.run(); err == nil {
				t.Logf("%s accepted unfinished published attempt %+v", check.name, attempt)
				allowed = true
			}
		}
		for _, operation := range []capability.DataOperation{capability.DataUpload, capability.DataCommit} {
			target := capability.DataTarget{Operation: operation, Digest: string(request.Intent.Digest), RunID: attempt.RunID, NodeID: attempt.NodeID, Attempt: attempt.Attempt, Generation: attempt.Generation}
			if err := tenant.CheckDataAccess(t.Context(), authority, target); err == nil {
				t.Logf("%s authorized unfinished published attempt %+v", operation, attempt)
				allowed = true
			}
		}
		if allowed {
			return false
		}
		rewriteSchemaCount(t, s.db, "blobs", existingBlobs)
		rewriteSchemaCount(t, s.db, "blob_receipts", 0)
		rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=2,outcome='success' WHERE team='a' AND run_id='run'")
		if err := tenant.AbortBlobUpload(t.Context(), input, ticket.ID); err != nil {
			t.Fatal(err)
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(2943))}); err != nil {
		t.Fatal(err)
	}
}
