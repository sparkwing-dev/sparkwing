package store

import (
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func commitTestBlob(t *testing.T, tenant *Tenant, intent blob.UploadIntent) (blob.Receipt, error) {
	t.Helper()
	rewriteSchemaExec(t, tenant.s.db, "INSERT INTO storage_quotas(team,updated_at) VALUES (?,1) ON CONFLICT(team) DO NOTHING", tenant.team)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: rand.Text(), Intent: intent, ExpiresAt: tenant.s.now().Add(time.Hour)})
	if err != nil {
		return blob.Receipt{}, err
	}
	return tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, intent.Authority))
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
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestBlobAccountingDiskConnectionsInitializeWALBeforeWrites(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	probe, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := probe.Close(); err != nil {
			t.Error(err)
		}
	})
	var mode string
	if err := probe.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("fixture returned before WAL initialization: %s", mode)
	}
}
