package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestSourceUploadQuotaExpiryAndUniqueAccounting(t *testing.T) {
	tenant, intake, now := intakeFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_quotas(team,max_bytes_per_month,updated_at) VALUES ('a',10,1)")
	u := SourceUpload{ID: "source_one", Scope: SourceUploadScope{Repository: "gh:1", ExecutionSHA: strings.Repeat("a", 40), PipelineSHA: strings.Repeat("a", 40)}, Blob: blob.Blob{Digest: blob.Digest(strings.Repeat("a", 64)), Size: 7}, ExpiresAt: now.Add(time.Minute)}
	first, err := tenant.ReserveSourceUpload(t.Context(), intake.Authority, u)
	if err != nil {
		t.Fatal(err)
	}
	u.ID = "source_two"
	if _, err := tenant.ReserveSourceUpload(t.Context(), intake.Authority, u); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("concurrent upload exceeded quota: %v", err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "source-expiry", 2*sourceBundleRetention)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	*now = now.Add(2 * time.Minute)
	if err := s.AsOperator().ExpireSourceUploads(t.Context(), lease, 10, objects); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitSourceUpload(t.Context(), intake.Authority, first.ID, first.Blob, "replica"); err == nil {
		t.Fatal("expired source committed")
	}
	u.ExpiresAt = now.Add(time.Minute)
	second, err := tenant.ReserveSourceUpload(t.Context(), intake.Authority, u)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := tenant.CommitSourceUpload(t.Context(), intake.Authority, second.ID, second.Blob, "replica"); err != nil {
			t.Fatal(err)
		}
	}
	var used, reserved int64
	if err := s.db.QueryRow("SELECT used,reserved FROM quota_admission WHERE team='a' AND quota='storage'").Scan(&used, &reserved); err != nil || used != 7 || reserved != 0 {
		t.Fatalf("usage=%d reserved=%d: %v", used, reserved, err)
	}
	*now = now.Add(sourceBundleRetention + time.Minute)
	if err := s.AsOperator().ExpireSourceUploads(t.Context(), lease, 10, objects); err != nil {
		t.Fatal(err)
	}
	marked, err := s.AsOperator().MarkBlobTombstones(t.Context(), lease, 10)
	if err != nil || marked != 1 {
		t.Fatalf("expired source root: marked=%d %v", marked, err)
	}
	rewriteSchemaCount(t, s.db, "source_uploads", 0)
}
