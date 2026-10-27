package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func checkStoreCASBoundary(t *testing.T) (err error) {
	ctx := t.Context()
	s, tenant, actor, attempt := resultFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	first := blobIntent(actor, attempt)
	identities := map[string]blob.UploadIntent{"a": first, "b": addBlobJob(t, s, first, "b"), "unrelated": addBlobJob(t, s, first, "unrelated")}
	path := blobAccountingDiskStore(t, s)
	reopen := func() error {
		if err := s.db.Close(); err != nil {
			return err
		}
		s.db = blobAccountingDB(t, path)
		return nil
	}
	request := UploadReservationRequest{ID: "interrupted", Intent: first, ExpiresAt: now.Add(time.Hour)}
	if err := tenant.SetStorageQuota(ctx, StorageQuota{MaxBytesPerMonth: 3, MaxBytesPerRun: 3, MaxObjectsPerRun: 1}); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte("abc"))
	request.Intent.Digest = blob.Digest(hex.EncodeToString(sum[:]))
	digest := request.Intent.Digest
	bind := func(job string) {
		request.Intent = identities[job]
		request.Intent.Digest = digest
	}
	bind("a")
	interrupted, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	commit := VerifiedUploadCommit{TicketID: interrupted.ID, Authority: request.Intent.Authority, Verified: interrupted.Blob}
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
	now = request.ExpiresAt
	if err := reopen(); err != nil {
		return err
	}
	if _, err := tenant.CommitBlobUpload(ctx, commit); !errors.Is(err, ErrLockHeld) {
		return fmt.Errorf("expired ticket committed after reopen: %v", err)
	}
	bind("b")
	request.ID, request.ExpiresAt = "replacement", now.Add(time.Hour)
	replacement, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return fmt.Errorf("expired reservation blocked replacement: %w", err)
	}
	commit.TicketID, commit.Authority, commit.Verified = replacement.ID, request.Intent.Authority, replacement.Blob
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
	return checkStorePresenceUsage(ctx, tenant)
}

func checkStoreCASRows(ctx context.Context, s *Store, want int) error {
	for _, table := range []string{"blobs", "blob_receipts"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE team=?", "a").Scan(&count); err != nil {
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

func checkStorePresenceUsage(ctx context.Context, tenant *Tenant) error {
	usage, err := tenant.StorageUsageFor(ctx, "run", StorageMonth(tenant.s.now()))
	if err != nil || usage.RunBytes != 3 || usage.MonthBytes != 3 || usage.RunObjects != 1 || usage.MonthObjects != 1 {
		return fmt.Errorf("upload usage: %+v: %v", usage, err)
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		var count int
		if err := tenant.s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE team=?", "a").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("presence consumed %s budget", table)
		}
	}
	return nil
}
