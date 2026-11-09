package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// ChildSourceBundleRegressions checks uploaded-source inheritance in the supplied store dialect.
func ChildSourceBundleRegressions(t *testing.T, open func(*testing.T) *Store) {
	t.Run("exact parent source", func(t *testing.T) { childSourceBundleRequiresExactParentSource(t, open) })
	t.Run("grandchild", func(t *testing.T) { childSourceBundleGrandchild(t, open) })
	t.Run("retry child", func(t *testing.T) { childSourceBundleRetry(t, open) })
}

func childSourceBundleRequiresExactParentSource(t *testing.T, open func(*testing.T) *Store) {
	for _, mutation := range []string{"none", "digest", "repository", "declared repository", "execution revision", "pipeline revision", "ref", "missing record", "foreign run record", "foreign team record", "unrelated submitter"} {
		t.Run(mutation, func(t *testing.T) {
			tenant, request := childSourceBundleFixture(t, open(t), mutation == "declared repository")
			s := tenant.s
			digest := request.Request.Source.BundleDigest.Value
			parent, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			code, message := apierr.Forbidden, "child source bundle differs from its parent's source"
			switch mutation {
			case "digest":
				request.Request.Source.BundleDigest.Value = strings.Repeat("b", 64)
			case "repository", "declared repository":
				request.Request.Git.Repository.Key = "gh:2"
				if mutation == "repository" {
					message = "child source is outside the parent claim's declared children; declare it on the parent job with StartsChildren (TypeScript: startsChildren), or StartsChildrenIn for another repository"
				}
			case "execution revision":
				request.Request.Git.SHA = strings.Repeat("b", 40)
			case "pipeline revision":
				request.Request.Source.Ref = strings.Repeat("b", 40)
			case "ref":
				request.Request.Git.Ref = "other"
			case "missing record":
				s.testOnlyExec(t, `DELETE FROM blob_records WHERE kind='source_bundle'`)
			case "foreign run record":
				s.testOnlyExec(t, `UPDATE blob_records SET run_id=NULL WHERE kind='source_bundle'`)
			case "foreign team record":
				s.testOnlyExec(t, `INSERT INTO blobs(team,digest,size,created_at) VALUES ('b',?,3,1)`, digest)
				s.testOnlyExec(t, `UPDATE blob_records SET team='b',run_id=NULL WHERE kind='source_bundle'`)
			case "unrelated submitter":
				message = "source bundle is not committed by this submitter for these revisions"
				request.Request.Lineage = runspec.Lineage{}
				root := authorityRoot("submitter", "a")
				root.Grants = []capability.Grant{capability.RunsWrite}
				authorityPersist(t, s, root)
				request.Authority.ID = root.Prefix
			}
			admitted, err := tenant.AdmitRun(t.Context(), request)
			if mutation != "none" {
				if mutation == "missing record" || mutation == "foreign run record" || mutation == "foreign team record" {
					code, message = apierr.IntegrityFailure, fmt.Sprintf("uploaded-bundle source record is missing for run %q and digest %q", "run", digest)
				}
				requireSourceBundleError(t, err, code, message)
				rewriteSchemaCount(t, s.db, "runs", 1)
				return
			}
			if err != nil {
				t.Fatalf("same-source child admission: %v", err)
			}
			requireChildSourceBundle(t, tenant, admitted.RunID, parent.Request.Provenance, digest)
			if replay, err := tenant.AdmitRun(t.Context(), request); err != nil || !replay.Replayed || replay.RunID != admitted.RunID {
				t.Fatalf("child replay: %+v: %v", replay, err)
			}
		})
	}
}

func childSourceBundleFixture(t *testing.T, s *Store, crossRepository bool) (*Tenant, IntakeRequest) {
	t.Helper()
	s.testOnlyExec(t, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1) ON CONFLICT(name) DO NOTHING")
	s.testOnlyExec(t, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,lineage_json,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'request',1,'gh:1','host','execute','account',1,?,'r30',30)`, []byte(`{}`), []byte(`{"admission_subject":{"kind":"account","id":"alice"}}`))
	seedSourceSetupBundle(t, s, strings.Repeat("a", 64))
	tenant := &Tenant{s: s, team: "a"}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	parent.Request.Request.Git.SHA = strings.Repeat("a", 40)
	parent.Request.Request.Source.Ref = parent.Request.Request.Git.SHA
	s.testOnlyExec(t, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, sealWith(t, parent.Request, parent.Request.Provenance))
	s.testOnlyExec(t, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	})
	children := []nodewire.ChildPipeline{{Pipeline: "child", Repository: "gh:1"}}
	if crossRepository {
		children = append(children, nodewire.ChildPipeline{Pipeline: "child", Repository: "gh:2"})
	}
	authority := sourceBundleChildJob(t, s, "run", children)
	return tenant, IntakeRequest{Authority: authority, IdempotencyKey: "invocation", Request: runspec.Request{Version: 1, Team: "a", Pipeline: "child", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: parent.Request.Request.Git, Source: parent.Request.Request.Source, Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}}}
}

func sourceBundleChildJob(t *testing.T, s *Store, run string, children []nodewire.ChildPipeline) capability.AuthorityRef {
	t.Helper()
	claim := authorityRoot("claim-"+run, "a")
	claim.Grants = []capability.Grant{capability.ChildrenStart}
	claim.Binding = capability.Binding{Kind: capability.BindingNode, RunID: run, NodeID: "job", Holder: "runner", Generation: 1}
	claim.Children = children
	authorityPersist(t, s, claim)
	s.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,attempt,started_at,lease_expires_at) VALUES ('a',?,'job','running',?,?,'runner',?,1,1,1,?)`, run, []byte(`{"id":"job"}`), []byte(`{}`), claim.Prefix, s.now().Add(time.Hour).UnixNano())
	return capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Prefix}
}

func requireSourceBundleError(t *testing.T, err error, code apierr.Code, message string) {
	t.Helper()
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != code || refusal.Message != message {
		t.Fatalf("source refusal = %v, want %s: %s", err, code, message)
	}
}

func requireChildSourceBundle(t *testing.T, tenant *Tenant, run string, parent runspec.Provenance, digest string) {
	t.Helper()
	child, err := tenant.ReadRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	proof := child.Request.Provenance
	if proof != parent || proof.Trust() != runspec.TrustAsserted || proof.ProvenRepository() != "" || proof.Verified() || proof.DefaultBranchWork() {
		t.Fatalf("child provenance = %+v, parent = %+v", proof, parent)
	}
	var boundDigest, sourceRun string
	if err := tenant.s.queryRow(t.Context(), `SELECT digest,source_run_id FROM blob_records WHERE team='a' AND run_id=? AND kind='source_bundle'`, run).Scan(&boundDigest, &sourceRun); err != nil || boundDigest != digest || sourceRun != "run" {
		t.Fatalf("source binding for %s: digest=%s source=%s: %v", run, boundDigest, sourceRun, err)
	}
}

func childSourceBundleGrandchild(t *testing.T, open func(*testing.T) *Store) {
	tenant, request := childSourceBundleFixture(t, open(t), false)
	child, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	tenant.s.testOnlyExec(t, `DELETE FROM blob_records WHERE team='a' AND run_id='run' AND kind='source_bundle'`)
	request.Authority = sourceBundleChildJob(t, tenant.s, child.RunID, []nodewire.ChildPipeline{{Pipeline: "grandchild", Repository: "gh:1"}})
	request.Request.Pipeline = "grandchild"
	request.Request.Lineage = runspec.Lineage{ParentRunID: child.RunID, ParentJobID: "job"}
	grandchild, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	requireChildSourceBundle(t, tenant, grandchild.RunID, child.Request.Provenance, request.Request.Source.BundleDigest.Value)
}

func childSourceBundleRetry(t *testing.T, open func(*testing.T) *Store) {
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprintf("matching digest=%t", matching), func(t *testing.T) {
			tenant, request := childSourceBundleFixture(t, open(t), false)
			child, err := tenant.AdmitRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			s := tenant.s
			s.testOnlyExec(t, `DELETE FROM blob_records WHERE team='a' AND run_id='run' AND kind='source_bundle'`)
			if !matching {
				digest := strings.Repeat("b", 64)
				s.testOnlyExec(t, `INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)`, digest)
				s.testOnlyExec(t, `UPDATE blob_records SET digest=? WHERE team='a' AND run_id=? AND kind='source_bundle'`, digest, child.RunID)
			}
			root := authorityRoot("retrier", "a")
			root.Grants = []capability.Grant{capability.RunsControl}
			authorityPersist(t, s, root)
			retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, RetryMode: "all", Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: child.RunID}}})
			digest := request.Request.Source.BundleDigest.Value
			if !matching {
				requireSourceBundleError(t, err, apierr.IntegrityFailure, fmt.Sprintf("uploaded-bundle source record is missing for run %q and digest %q", child.RunID, digest))
				rewriteSchemaCount(t, s.db, "runs", 2)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requireChildSourceBundle(t, tenant, retry.RunID, child.Request.Provenance, digest)
			run, err := tenant.ReadRun(t.Context(), retry.RunID)
			if err != nil || run.Lineage.RetryOf != child.RunID {
				t.Fatalf("retry lineage = %+v: %v", run.Lineage, err)
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
