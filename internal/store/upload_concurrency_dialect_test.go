package store_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestBlobUploadCommitAbortContention(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { checkUploadCommitAbortContention(t, storetest.NewSQLite(t)) })
	t.Run("postgres", func(t *testing.T) { checkUploadCommitAbortContention(t, storetest.NewPostgres(t)) })
}

func checkUploadCommitAbortContention(t *testing.T, target *storetest.Target) {
	t.Helper()
	f := newStoreRecoveryFixtureAt(t, true, target)
	other := target.Open(t)
	clients := []*store.Store{f.s, other}
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(f.tenant.Team()), RunID: f.ref.RunID, NodeID: f.ref.NodeID, Attempt: int64(row.Attempt), Generation: int64(row.Generation)}}
	authority := blob.Authority{CapabilityRef: f.holder.CapabilityPrefix, Attempt: attempt}
	identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: string(f.tenant.Team()), RunID: f.ref.RunID, Attempt: attempt, Principal: blob.Principal(f.holder.Holder), CapabilityRef: f.holder.CapabilityPrefix}
	for sample := range 4 {
		request := store.UploadReservationRequest{ID: fmt.Sprintf("contended_%d", sample), Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat(fmt.Sprint(sample+1), 64)), Size: int64(sample + 1)}, ExpiresAt: time.Now().Add(time.Minute)}
		start := make(chan struct{})
		type result struct {
			ticket store.UploadReservation
			err    error
		}
		reservations := make(chan result, 2)
		for _, client := range clients {
			go func() {
				<-start
				tenant, e := client.ForTeam(t.Context(), f.tenant.Team())
				if e != nil {
					reservations <- result{err: e}
					return
				}
				ticket, e := tenant.ReserveBlobUpload(t.Context(), request)
				reservations <- result{ticket, e}
			}()
		}
		close(start)
		var ticket store.UploadReservation
		for range 2 {
			r := <-reservations
			if r.err != nil {
				t.Fatal(r.err)
			}
			ticket = r.ticket
		}
		second, err := other.ForTeam(t.Context(), f.tenant.Team())
		if err != nil {
			t.Fatal(err)
		}
		start = make(chan struct{})
		committed, aborted := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, e := f.tenant.CommitBlobUpload(t.Context(), store.VerifiedUploadCommit{Authority: authority, TicketID: ticket.ID, Verified: ticket.Blob, ReplicaID: "first"})
			committed <- e
		}()
		go func() { <-start; aborted <- second.AbortBlobUpload(t.Context(), authority, ticket.ID) }()
		close(start)
		commitErr, abortErr := <-committed, <-aborted
		if abortErr != nil || commitErr != nil && !errors.Is(commitErr, store.ErrLockHeld) {
			t.Fatalf("commit=%v abort=%v", commitErr, abortErr)
		}
		var receipts, holds, orphans int
		for _, check := range []struct {
			query string
			into  *int
		}{
			{"SELECT COUNT(*) FROM blob_receipts WHERE team=? AND digest=?", &receipts},
			{"SELECT COUNT(*) FROM quota_reservations WHERE team=? AND id=? AND state='held'", &holds},
			{"SELECT COUNT(*) FROM blob_tombstones WHERE team=? AND digest=?", &orphans},
		} {
			key := string(ticket.Blob.Digest)
			if check.into == &holds {
				key = "upload:" + ticket.ID
			}
			if err := other.DB().QueryRowContext(t.Context(), storetest.Rebind(other, check.query), f.tenant.Team(), key).Scan(check.into); err != nil {
				t.Fatal(err)
			}
		}
		wantReceipt := 1
		if commitErr != nil {
			wantReceipt = 0
		}
		if receipts != wantReceipt || holds != 0 || orphans != 1-wantReceipt {
			t.Fatalf("receipt=%d holds=%d orphans=%d, committed=%t", receipts, holds, orphans, commitErr == nil)
		}
	}
}
