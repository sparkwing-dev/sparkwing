package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func publishedLogFixture(t *testing.T) (*Store, *Tenant, SegmentCommit) {
	t.Helper()
	s, tenant, writer, attempt := resultFixture(t)
	root := authorityRoot("publisher-root", "a")
	root.Subject.ID = "machine"
	root.Grants = []capability.Grant{capability.DataWrite}
	authorityPersist(t, s, root)
	var publisher capability.Capability
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		publisher, err = loadCapabilityTx(t.Context(), tx, writer.CapabilityPrefix)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	publisher.Prefix, publisher.Digest = "publisher", [32]byte{1}
	publisher.Issuance = capability.Delegate
	publisher.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	publisher.Grants = []capability.Grant{capability.DataWrite}
	publisher.Unit, publisher.UnitID, publisher.Purpose, publisher.Delivery = "", "", "", ""
	publisher.Billing = nil
	publisher.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
	publisher.Data.Use = capability.DataPublish
	authorityPersist(t, s, publisher)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET intake_mode='publish',status='success',finished_at=2 WHERE team='a' AND id='run'")
	rewriteSchemaExec(t, s.db, "DELETE FROM triggers WHERE team='a' AND id='run'")
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET status='done',outcome='success',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,finished_at=2 WHERE team='a' AND run_id='run'")
	rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET started_at=1,finished_at=2,outcome='success',executor_location='local' WHERE team='a' AND run_id='run'")
	actor := Actor{Kind: ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: publisher.Prefix}, CapabilityPrefix: publisher.Prefix}
	return s, tenant, logCommit(actor, attempt)
}

func TestLogPersistencePublishedAttemptsNeedNoExecutionLease(t *testing.T) {
	s, tenant, commit := publishedLogFixture(t)
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatalf("published log after local completion: %v", err)
	}
	commit.Final = true
	commit.IncompleteReason = "local process lost its tail"
	for range 2 {
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
	}
	reader := authorityMember(t, s, "published-log-reader", RoleReader)
	got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: commit.Subject})
	if err != nil || got.Complete || got.IncompleteReason != commit.IncompleteReason || len(got.Segments) != 1 || got.Segments[0] != commit.Segments[0] {
		t.Fatalf("published log: %+v, %v", got, err)
	}
	var status, outcome string
	if err := s.db.QueryRow("SELECT status,outcome FROM nodes WHERE team='a' AND run_id='run'").Scan(&status, &outcome); err != nil || status != "done" || outcome != "success" {
		t.Fatalf("log completeness changed result: %s/%s: %v", status, outcome, err)
	}
	commit.IncompleteReason = "changed tail"
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("publisher rewrote sealed logs")
	}
}

func TestLogPersistencePublishedAttemptsRecheckAuthority(t *testing.T) {
	for _, changed := range []string{"executable_run", "unimported_attempt", "generation", "open_attempt", "revoked_parent", "expired", "foreign_repo", "foreign_run", "missing_grant", "claim_scope", "owner_subject"} {
		t.Run(changed, func(t *testing.T) {
			s, tenant, commit := publishedLogFixture(t)
			switch changed {
			case "executable_run":
				rewriteSchemaExec(t, s.db, "UPDATE runs SET intake_mode='execute' WHERE team='a' AND id='run'")
			case "unimported_attempt":
				commit.Subject.Attempt.Attempt++
			case "generation":
				commit.Subject.Attempt.Generation++
			case "open_attempt":
				rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=NULL,outcome='' WHERE team='a' AND run_id='run'")
			case "revoked_parent":
				rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=1 WHERE prefix='publisher-root'")
			case "owner_subject":
				commit.Subject.Kind, commit.Subject.Attempt = blob.LogOwnerUnit, nil
				commit.Subject.Unit = &blob.LogUnit{Kind: blob.LogPlan, Generation: 7}
			default:
				var c capability.Capability
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					var err error
					c, err = loadCapabilityTx(t.Context(), tx, commit.Actor.CapabilityPrefix)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				switch changed {
				case "expired":
					at := s.now().Add(-time.Second)
					c.IssuedAt = at.Add(-time.Hour)
					c.ExpiresAt = &at
				case "foreign_repo":
					c.Data.RepositoryKey = "gh:other"
				case "foreign_run":
					rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) SELECT team,'other',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
					c.Binding.RunID, c.Data.RunID = "other", "other"
				case "missing_grant":
					c.Grants = nil
				case "claim_scope":
					c.Data.Use = capability.DataClaim
				}
				c.Prefix = "modified-publisher"
				c.Digest = [32]byte{2}
				commit.Actor.Authority.ID, commit.Actor.CapabilityPrefix = c.Prefix, c.Prefix
				authorityPersist(t, s, c)
			}
			key, err := blob.LogObjectKey(commit.Subject, commit.RetentionClass, 1)
			if err != nil {
				t.Fatal(err)
			}
			commit.Segments[0].ObjectKey = key
			if err := tenant.RecordSegments(t.Context(), commit); err == nil {
				t.Fatal("unauthorized publication committed logs")
			}
			rewriteSchemaCount(t, s.db, "log_streams", 0)
			rewriteSchemaCount(t, s.db, "log_segments", 0)
		})
	}
}
