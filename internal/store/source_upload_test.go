package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestChildSourceBundleRequiresExactParentSource(t *testing.T) {
	for _, mutation := range []string{"none", "digest", "repository", "execution revision", "pipeline revision", "ref", "missing record", "foreign run record", "foreign team record", "unrelated submitter"} {
		t.Run(mutation, func(t *testing.T) {
			tenant, request := childIntakeFixture(t)
			s := tenant.s
			digest := strings.Repeat("a", 64)
			seedSourceSetupBundle(t, s, digest)
			parent, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			parent.Request.Request.Git.SHA = request.Request.Git.SHA
			parent.Request.Request.Source.Ref = request.Request.Source.Ref
			rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, sealWith(t, parent.Request, parent.Request.Provenance))
			request.Request.Git = parent.Request.Request.Git
			request.Request.Source = parent.Request.Request.Source
			switch mutation {
			case "digest":
				request.Request.Source.BundleDigest.Value = strings.Repeat("b", 64)
			case "repository":
				request.Request.Git.Repository.Key = "gh:2"
			case "execution revision":
				request.Request.Git.SHA = strings.Repeat("b", 40)
			case "pipeline revision":
				request.Request.Source.Ref = strings.Repeat("b", 40)
			case "ref":
				request.Request.Git.Ref = "other"
			case "missing record":
				rewriteSchemaExec(t, s.db, `DELETE FROM blob_records WHERE kind='source_bundle'`)
			case "foreign run record":
				rewriteSchemaExec(t, s.db, `UPDATE blob_records SET run_id=NULL WHERE kind='source_bundle'`)
			case "foreign team record":
				rewriteSchemaExec(t, s.db, `INSERT INTO blobs(team,digest,size,created_at) VALUES ('b',?,3,1)`, digest)
				rewriteSchemaExec(t, s.db, `UPDATE blob_records SET team='b',run_id=NULL WHERE kind='source_bundle'`)
			case "unrelated submitter":
				request.Request.Lineage = runspec.Lineage{}
				root := authorityRoot("submitter", "a")
				root.Grants = []capability.Grant{capability.RunsWrite}
				authorityPersist(t, s, root)
				request.Authority.ID = root.Prefix
			}
			admitted, err := tenant.AdmitRun(t.Context(), request)
			if mutation != "none" {
				if err == nil {
					t.Fatal("unrelated source bundle accepted")
				}
				rewriteSchemaCount(t, s.db, "runs", 1)
				return
			}
			if err != nil {
				t.Fatalf("same-source child admission: %v", err)
			}
			var boundDigest, sourceRun string
			if err := s.db.QueryRow(`SELECT digest,source_run_id FROM blob_records WHERE team='a' AND run_id=? AND kind='source_bundle'`, admitted.RunID).Scan(&boundDigest, &sourceRun); err != nil || boundDigest != digest || sourceRun != "run" {
				t.Fatalf("child source binding: digest=%s source=%s: %v", boundDigest, sourceRun, err)
			}
			if replay, err := tenant.AdmitRun(t.Context(), request); err != nil || !replay.Replayed || replay.RunID != admitted.RunID {
				t.Fatalf("child replay: %+v: %v", replay, err)
			}
		})
	}
}

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
