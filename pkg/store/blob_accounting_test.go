package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func reserveBlobTestQuota(t *testing.T, tenant *Tenant, size int64) BlobAccounting {
	t.Helper()
	rewriteSchemaExec(t, tenant.s.db, "INSERT INTO storage_quotas(team,updated_at) VALUES (?,?) ON CONFLICT(team) DO NOTHING", tenant.team, tenant.s.now().UnixNano())
	reservation, err := tenant.ReserveQuota(t.Context(), "storage", size)
	if err != nil {
		t.Fatal(err)
	}
	return BlobAccounting{ReservationID: reservation.ID, ReplicaID: "test-replica"}
}

func assertBlobUsage(t *testing.T, s *Store, used, reserved, bytes, objects int64) {
	t.Helper()
	var gotUsed, gotReserved, gotBytes, gotObjects int64
	if err := s.db.QueryRow("SELECT used,reserved FROM quota_admission WHERE team='a' AND quota='storage'").Scan(&gotUsed, &gotReserved); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COALESCE(SUM(bytes),0),COALESCE(SUM(objects),0) FROM storage_month_usage WHERE team='a'").Scan(&gotBytes, &gotObjects); err != nil {
		t.Fatal(err)
	}
	if gotUsed != used || gotReserved != reserved || gotBytes != bytes || gotObjects != objects {
		t.Fatalf("quota=(%d,%d) usage=(%d,%d); want (%d,%d) (%d,%d)", gotUsed, gotReserved, gotBytes, gotObjects, used, reserved, bytes, objects)
	}
}

func TestBlobAccountingCommitReplayAndAbort(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	accounting := reserveBlobTestQuota(t, tenant, intent.Size)
	aborted := reserveBlobTestQuota(t, tenant, intent.Size)
	verify := blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })
	for range 2 {
		if _, err := tenant.CommitBlob(t.Context(), intent, accounting, verify); err != nil {
			t.Fatal(err)
		}
		if err := tenant.SettleQuota(t.Context(), aborted.ReservationID, 0); err != nil {
			t.Fatal(err)
		}
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
	if _, err := tenant.CommitBlob(t.Context(), intent, aborted, verify); err == nil {
		t.Fatal("released reservation committed")
	}
	intent.Digest = blob.Digest(strings.Repeat("c", 64))
	if _, err := tenant.CommitBlob(t.Context(), intent, accounting, verify); err == nil {
		t.Fatal("settled reservation funded another blob")
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
	rewriteSchemaCount(t, s.db, "blobs", 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 1)
}

func TestBlobAccountingUniqueBytesAcrossUploadersAndReplicas(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	first := blobIntent(actor, attempt)
	second := addBlobJob(t, s, first, "second")
	for i, intent := range []blob.UploadIntent{first, second} {
		accounting := reserveBlobTestQuota(t, tenant, intent.Size)
		if i == 1 {
			accounting.ReplicaID = "replica-b"
		}
		for range 2 {
			if _, err := tenant.CommitBlob(t.Context(), intent, accounting, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 2)
}

func TestBlobAccountingAtomicRollback(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	accounting := reserveBlobTestQuota(t, tenant, intent.Size)
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER fail_blob_usage BEFORE INSERT ON storage_month_usage BEGIN SELECT RAISE(ABORT,'injected usage failure'); END")
	verify := blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })
	if _, err := tenant.CommitBlob(t.Context(), intent, accounting, verify); err == nil {
		t.Fatal("usage failure ignored")
	}
	assertBlobUsage(t, s, 0, 3, 0, 0)
	rewriteSchemaCount(t, s.db, "blobs", 0)
	rewriteSchemaCount(t, s.db, "blob_receipts", 0)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER fail_blob_usage")
	if _, err := tenant.CommitBlob(t.Context(), intent, accounting, verify); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
}

func TestBlobAccountingRejectsInvalidReservation(t *testing.T) {
	for _, problem := range []string{"missing", "foreign", "download", "undersized", "expired", "replica"} {
		t.Run(problem, func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			intent := blobIntent(actor, attempt)
			accounting := reserveBlobTestQuota(t, tenant, intent.Size)
			switch problem {
			case "missing":
				accounting.ReservationID = "absent"
			case "foreign":
				other := &Tenant{s: s, team: "b"}
				accounting = reserveBlobTestQuota(t, other, intent.Size)
			case "download":
				reservation, err := tenant.ReserveQuota(t.Context(), "download", intent.Size)
				if err != nil {
					t.Fatal(err)
				}
				accounting.ReservationID = reservation.ID
			case "undersized":
				intent.Size++
			case "expired":
				rewriteSchemaExec(t, s.db, "UPDATE quota_reservations SET expires_at=? WHERE team='a'", s.now().UnixNano())
			case "replica":
				accounting.ReplicaID = ""
			}
			if _, err := tenant.CommitBlob(t.Context(), intent, accounting, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })); err == nil {
				t.Fatal("invalid reservation accepted")
			}
			rewriteSchemaCount(t, s.db, "blobs", 0)
			rewriteSchemaCount(t, s.db, "blob_receipts", 0)
		})
	}
}

func TestBlobAccountingVerificationFailureCanReleaseReservation(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	accounting := reserveBlobTestQuota(t, tenant, intent.Size)
	if _, err := tenant.CommitBlob(t.Context(), intent, accounting, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return blob.ErrIntegrity })); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("verification: %v", err)
	}
	for range 2 {
		if err := tenant.SettleQuota(t.Context(), accounting.ReservationID, 0); err != nil {
			t.Fatal(err)
		}
	}
	assertBlobUsage(t, s, 0, 0, 0, 0)
}

func TestBlobAccountingReplayAfterReservationExpires(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	accounting := reserveBlobTestQuota(t, tenant, intent.Size)
	verify := blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })
	if _, err := tenant.CommitBlob(t.Context(), intent, accounting, verify); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE quota_reservations SET expires_at=? WHERE team='a'", s.now().Add(-time.Second).UnixNano())
	if _, err := tenant.CommitBlob(t.Context(), intent, accounting, verify); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
}

func blobAccountingDiskStore(t *testing.T, s *Store) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounting.db")
	rewriteSchemaExec(t, s.db, "VACUUM INTO ?", path)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	return path
}

func blobAccountingDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func TestBlobAccountingDurableCommitAndAbortReplay(t *testing.T) {
	for _, checkpoint := range []string{"before", "after"} {
		t.Run(checkpoint, func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			path := blobAccountingDiskStore(t, s)
			intent := blobIntent(actor, attempt)
			committed := reserveBlobTestQuota(t, tenant, intent.Size)
			aborted := reserveBlobTestQuota(t, tenant, intent.Size)
			settle := func() {
				t.Helper()
				if _, err := tenant.CommitBlob(t.Context(), intent, committed, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })); err != nil {
					t.Fatal(err)
				}
				if err := tenant.SettleQuota(t.Context(), aborted.ReservationID, 0); err != nil {
					t.Fatal(err)
				}
			}
			if checkpoint == "after" {
				settle()
			}
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = blobAccountingDB(t, path)
			settle()
			settle()
			assertBlobUsage(t, s, 3, 0, 3, 1)
		})
	}
}

func TestBlobAccountingIndependentClientsDeduplicate(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	otherTenant := &Tenant{s: other, team: "a"}
	first := blobIntent(actor, attempt)
	second := addBlobJob(t, s, first, "second")
	accounting := []BlobAccounting{reserveBlobTestQuota(t, tenant, 3), reserveBlobTestQuota(t, otherTenant, 3)}
	accounting[1].ReplicaID = "replica-b"
	start, results := make(chan struct{}), make(chan error, 2)
	for i, intent := range []blob.UploadIntent{first, second} {
		writer := []*Tenant{tenant, otherTenant}[i]
		go func() {
			<-start
			_, err := writer.CommitBlob(t.Context(), intent, accounting[i], blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil }))
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 2)
}

func TestBlobAccountingEmptyBlobStillCommitsReservation(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	intent.Size = 0
	accounting := reserveBlobTestQuota(t, tenant, 0)
	for range 2 {
		if _, err := tenant.CommitBlob(t.Context(), intent, accounting, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })); err != nil {
			t.Fatal(err)
		}
	}
	assertBlobUsage(t, s, 0, 0, 0, 1)
	var state string
	if err := s.db.QueryRow("SELECT state FROM quota_reservations WHERE team='a' AND id=?", accounting.ReservationID).Scan(&state); err != nil || state != "committed" {
		t.Fatalf("state=%s: %v", state, err)
	}
}
