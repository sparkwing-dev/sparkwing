package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func FuzzSourceUploadExpiryTenantScope(f *testing.F) {
	f.Add("same-id", false)
	f.Add("same' OR 1=1 --", true)
	f.Fuzz(func(t *testing.T, id string, reverse bool) {
		s := authorityStore(t)
		team, other := Team("a"), Team("b")
		if reverse {
			team, other = other, team
		}
		for _, key := range []Team{team, other} {
			rewriteSchemaExec(t, s.db, `INSERT INTO source_uploads(team,id,authority_kind,authority_id,repo_key,repo_display,execution_sha,pipeline_sha,digest,size,expires_at,committed_at) VALUES (?,?,'capability','actor','gh:1','','sha','sha','digest',0,1,1)`, key, id)
		}
		tx, err := s.beginTx(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackOrLog(tx)
		if err := expireSourceUploadTx(t.Context(), tx, team, id); err != nil {
			t.Fatal(err)
		}
		var remaining Team
		if err := tx.QueryRowContext(t.Context(), "SELECT team FROM source_uploads WHERE id=?", id).Scan(&remaining); err != nil || remaining != other {
			t.Fatalf("foreign upload changed: team=%s: %v", remaining, err)
		}
	})
}

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
	if err := s.AsOperator().ExpireSourceUploads(t.Context(), 10, objects); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitSourceUpload(t.Context(), intake.Authority, first.ID, first.Blob); err == nil {
		t.Fatal("expired source committed")
	}
	u.ExpiresAt = now.Add(time.Minute)
	second, err := tenant.ReserveSourceUpload(t.Context(), intake.Authority, u)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := tenant.CommitSourceUpload(t.Context(), intake.Authority, second.ID, second.Blob); err != nil {
			t.Fatal(err)
		}
	}
	var used, reserved int64
	if err := s.db.QueryRow("SELECT used,reserved FROM quota_admission WHERE team='a' AND quota='storage'").Scan(&used, &reserved); err != nil || used != 7 || reserved != 0 {
		t.Fatalf("usage=%d reserved=%d: %v", used, reserved, err)
	}
	*now = now.Add(sourceBundleRetention + time.Minute)
	if err := s.AsOperator().ExpireSourceUploads(t.Context(), 10, objects); err != nil {
		t.Fatal(err)
	}
	marked, err := s.AsOperator().MarkBlobTombstones(t.Context(), 10)
	if err != nil || marked != 1 {
		t.Fatalf("expired source root: marked=%d %v", marked, err)
	}
	rewriteSchemaCount(t, s.db, "source_uploads", 0)
}
