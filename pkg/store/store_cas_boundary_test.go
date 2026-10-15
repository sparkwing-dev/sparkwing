package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func checkStoreCASBoundary(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := teststore.SeedPresence(ctx, s.DB())
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	reopen := func() error {
		if err := s.Close(); err != nil {
			return err
		}
		next, err := target.TryOpen()
		if err != nil {
			return err
		}
		s = next
		tenant, err = s.ForTeam(ctx, store.DefaultTeam)
		return err
	}
	if err := tenant.SetStorageQuota(ctx, store.StorageQuota{MaxBytesPerMonth: 3, MaxBytesPerRun: 3, MaxObjectsPerRun: 1}); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte("abc"))
	request.Intent.Digest = blob.Digest(hex.EncodeToString(sum[:]))
	bind := func(job string) {
		selected := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: job, Attempt: 1, Generation: 7}}
		request.Intent.Authority.Attempt, request.Intent.Identity.Attempt = selected, selected
	}
	bind("a")
	interrupted, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	commit := store.VerifiedUploadCommit{TicketID: interrupted.ID, Authority: request.Intent.Authority, Verified: interrupted.Blob, ReplicaID: "first"}
	for _, changed := range []string{"digest", "short", "long", "team", "ticket"} {
		bad := commit
		switch changed {
		case "digest":
			bad.Verified.Digest = blob.Digest("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
		case "short":
			bad.Verified.Size--
		case "long":
			bad.Verified.Size++
		case "team":
			bad.Verified.Team = "foreign"
		case "ticket":
			bad.TicketID = "missing"
		}
		if _, err := tenant.CommitBlobUpload(ctx, bad); err == nil {
			return fmt.Errorf("%s verification evidence committed", changed)
		}
		if err := checkStoreCASRows(ctx, s, 0); err != nil {
			return err
		}
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE uploads SET expires_at=1 WHERE team=?", store.DefaultTeam); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE quota_reservations SET expires_at=1 WHERE team=?", store.DefaultTeam); err != nil {
		return err
	}
	if err := reopen(); err != nil {
		return err
	}
	if _, err := tenant.CommitBlobUpload(ctx, commit); !errors.Is(err, store.ErrLockHeld) {
		return fmt.Errorf("expired ticket committed after reopen: %v", err)
	}
	bind("b")
	request.ID = "replacement"
	replacement, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return fmt.Errorf("expired reservation blocked replacement: %w", err)
	}
	commit.TicketID, commit.Authority, commit.Verified, commit.ReplicaID = replacement.ID, request.Intent.Authority, replacement.Blob, "replacement"
	if _, err := s.DB().ExecContext(ctx, `CREATE TRIGGER refuse_cas_receipt BEFORE INSERT ON blob_receipts BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`); err != nil {
		return err
	}
	if _, err := tenant.CommitBlobUpload(ctx, commit); err == nil {
		return errors.New("failed receipt transaction committed existence")
	}
	if err := checkStoreCASRows(ctx, s, 0); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "DROP TRIGGER refuse_cas_receipt"); err != nil {
		return err
	}
	var receipt blob.Receipt
	for range 2 {
		got, err := tenant.CommitBlobUpload(ctx, commit)
		if err != nil || got.Identity != request.Intent.Identity || got.Digest != request.Intent.Digest {
			return fmt.Errorf("replacement receipt=%+v: %v", got, err)
		}
		if receipt.Identity.Team != "" && receipt != got {
			return errors.New("receipt changed on commit replay")
		}
		receipt = got
	}
	if err := reopen(); err != nil {
		return err
	}
	for _, job := range []string{"a", "b", "unrelated"} {
		bind(job)
		presence, err := tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: request.Intent.Authority, Digests: []blob.Digest{request.Intent.Digest}})
		if err != nil || (len(presence.Present) == 1) != (job == "b") {
			return fmt.Errorf("replacement receipt transferred to %s: %+v: %v", job, presence, err)
		}
	}
	if err := checkStoreCASRows(ctx, s, 1); err != nil {
		return err
	}
	return checkStorePresenceUsage(ctx, tenant, s.DB())
}

func checkStoreCASRows(ctx context.Context, s *store.Store, want int) error {
	for _, table := range []string{"blobs", "blob_receipts"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE team=?", store.DefaultTeam).Scan(&count); err != nil {
			return err
		}
		if count != want {
			return fmt.Errorf("%s has %d rows, want %d", table, count, want)
		}
	}
	return nil
}

func TestStoreCASIntegrity(t *testing.T) {
	if err := checkStoreCASBoundary(t); err != nil {
		t.Fatal(err)
	}
}
