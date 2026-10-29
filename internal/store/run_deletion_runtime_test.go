package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAuthorizedRunDeletionPreservesBillingAndAllowsCleanupReplay(t *testing.T) {
	for _, deletingRole := range []string{"owner-other", "editor-own"} {
		t.Run(deletingRole, func(t *testing.T) {
			s := storetest.OpenSQLite(t)
			tenant, finish := runCompletionFixture(t, s)
			row, err := tenant.ReadRun(t.Context(), string(finish.Unit.RunID))
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.AsOperator().ResolveAuthority(t.Context(), row.Request.AuthorityRef)
			if err != nil {
				t.Fatal(err)
			}
			route, _ := capability.LookupRoute("DELETE", "/api/v1/runs/{run}")
			target := capability.Target{Team: string(tenant.Team()), RunID: row.ID}
			a, err := capability.Authorize(t.Context(), s.AsOperator(), route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, target)
			if err != nil {
				t.Fatal(err)
			}
			var conflict *apierr.Error
			if err := tenant.DeleteAuthorizedRun(t.Context(), a, nil); !errors.As(err, &conflict) || conflict.Code != apierr.Conflict {
				t.Fatalf("active deletion: %v", err)
			}
			if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: finish.Transition.RunID, Actor: finish.Transition.Actor, Cause: runstate.RunFinish}); err != nil {
				t.Fatal(err)
			}
			if err := tenant.DeleteAuthorizedRun(t.Context(), a, nil); err == nil {
				t.Fatal("terminal run with an unsettled owner was deleted")
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100, Reference: "deletion-fixture", CreatedBy: "operator"}); err != nil {
				t.Fatal(err)
			}
			var grants, awards, intervals int
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM billing_awards`).Scan(&awards); err != nil {
				t.Fatal(err)
			}
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM billable_intervals`).Scan(&intervals); err != nil {
				t.Fatal(err)
			}
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM credit_grants`).Scan(&grants); err != nil || grants == 0 {
				t.Fatalf("missing financial facts: %v", err)
			}
			// A different editor can control runs, but cannot erase their owner's history.
			for _, query := range []string{
				`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('other-editor','other-editor@example.test',1,1)`,
				`INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,'other-editor','editor',1)`,
				`INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('other-editor','other-editor','runs.read,runs.write,runs.control',1,4102444800000000000,?,'other-editor')`,
				`INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('other-editor',?,0)`,
			} {
				var err error
				if strings.Contains(query, "?") {
					_, err = s.DB().Exec(query, tenant.Team())
				} else {
					_, err = s.DB().Exec(query)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			editor, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "other-editor"})
			if err != nil {
				t.Fatal(err)
			}
			forbidden, err := capability.Authorize(t.Context(), s.AsOperator(), route, capability.Authenticated{Principal: editor, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := tenant.DeleteAuthorizedRun(t.Context(), forbidden, nil); err == nil {
				t.Fatal("editor deleted another account's run")
			}
			if _, err := s.DB().Exec(`INSERT INTO audit_events(id,ts,team,kind,subject_id,run_id) VALUES ('secret-audit',1,?,'secret.released','secret-release',?),('git-audit',1,?,'git_credential.released','git-release',?)`, tenant.Team(), row.ID, tenant.Team(), row.ID); err != nil {
				t.Fatal(err)
			}
			other := tenantFor(t, s, "other")
			if err := other.DeleteAuthorizedRun(t.Context(), a, nil); err == nil {
				t.Fatal("foreign handle deleted run")
			}
			if deletingRole == "owner-other" {
				if _, err := s.DB().Exec(`UPDATE memberships SET role='owner' WHERE team=? AND account_id='other-editor'`, tenant.Team()); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB().Exec(`UPDATE sessions SET scopes='runs.read,runs.write,runs.control,team.admin' WHERE hash='other-editor'`); err != nil {
					t.Fatal(err)
				}
				a = forbidden
			} else {
				if _, err := s.DB().Exec(`UPDATE memberships SET role='editor' WHERE team=? AND account_id=?`, tenant.Team(), p.Capability.Subject.ID); err != nil {
					t.Fatal(err)
				}
			}
			// Log objects go before the records: a failed delete keeps the run and its counted log slot.
			if _, err := s.DB().Exec(`INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,prefix,retention_class,created_at,updated_at) VALUES (?,?,'stream','owner_unit',1,'plan','stream','r30',1,1)`, tenant.Team(), row.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`INSERT INTO log_slots(team,run_id,stream_id,generation,n,size,committed,object_key,created_at) VALUES (?,?,'stream',1,1,8,1,'key',?)`, tenant.Team(), row.ID, teststore.Now(s).UnixNano()); err != nil {
				t.Fatal(err)
			}
			// A live upload deadline refuses deletion: a saved URL could recreate the deleted object uncounted.
			var uploading *apierr.Error
			if err := tenant.DeleteAuthorizedRun(t.Context(), a, func(context.Context) error { t.Fatal("logs deleted while still uploading"); return nil }); !errors.As(err, &uploading) || uploading.Code != apierr.Conflict || !strings.Contains(uploading.Message, "still uploading") {
				t.Fatalf("deletion within the upload deadline = %v; want a conflict", err)
			}
			if _, err := s.DB().Exec(`UPDATE log_slots SET created_at=? WHERE team=? AND run_id=?`, teststore.Now(s).Add(-limits.LogUploadURLTTL-time.Second).UnixNano(), tenant.Team(), row.ID); err != nil {
				t.Fatal(err)
			}
			retained, err := tenant.StorageRetainedBytes(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			failing := func(context.Context) error { return errors.New("object store unavailable") }
			if err := tenant.DeleteAuthorizedRun(t.Context(), a, failing); err == nil {
				t.Fatal("run deleted although its log objects were not")
			}
			if after, err := tenant.StorageRetainedBytes(t.Context()); err != nil || after != retained {
				t.Fatalf("retained after a failed log delete = %d, want %d: %v", after, retained, err)
			}
			if _, err := tenant.ReadRun(t.Context(), row.ID); err != nil {
				t.Fatalf("run lost after a failed log delete: %v", err)
			}
			deletes := 0
			for range 2 {
				if err := tenant.DeleteAuthorizedRun(t.Context(), a, func(context.Context) error { deletes++; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if deletes != 1 {
				t.Fatalf("log objects deleted %d times; want once, before the records", deletes)
			}
			if _, err := tenant.ReadRun(t.Context(), row.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("deleted run: %v", err)
			}
			var audits int
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE run_id=? AND kind IN ('secret.released','git_credential.released','run.deleted')`, row.ID).Scan(&audits); err != nil || audits != 3 {
				t.Fatalf("retained release audits and tombstone=%d: %v", audits, err)
			}
			var kept int
			for query, want := range map[string]int{`SELECT COUNT(*) FROM credit_grants`: grants, `SELECT COUNT(*) FROM billing_awards`: awards, `SELECT COUNT(*) FROM billable_intervals`: intervals} {
				if err := s.DB().QueryRow(query).Scan(&kept); err != nil || kept != want {
					t.Fatalf("billing rows=%d, want=%d: %v", kept, want, err)
				}
			}
			if err := tenant.RevokeAuthority(t.Context(), runstate.Actor{Authority: a.Principal.Authority}, a.Principal.Authority); err != nil {
				t.Fatal(err)
			}
			if err := tenant.DeleteAuthorizedRun(t.Context(), a, nil); err == nil {
				t.Fatal("revoked authority retried cleanup")
			}
		})
	}
}

func TestReusedRunIDRetainsEveryDeletionAuditSQLite(t *testing.T) {
	assertReusedRunIDRetainsEveryDeletionAudit(t, storetest.NewSQLite(t).Open(t))
}

func TestReusedRunIDRetainsEveryDeletionAuditPostgres(t *testing.T) {
	assertReusedRunIDRetainsEveryDeletionAudit(t, storetest.NewPostgres(t).Open(t))
}

func assertReusedRunIDRetainsEveryDeletionAudit(t *testing.T, s *store.Store) {
	t.Helper()
	owner := signIn(t, s, "owner", "owner@example.test")
	tn := tenant(t, s, owner.PersonalTeam)
	actor := membershipActor(t, s, owner.Account.ID, tn.Team())
	p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("DELETE", "/api/v1/runs/{run}")
	for range 2 {
		_, err := s.DB().Exec(storetest.Rebind(s, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES (?,'reused',1,'p','success',?,'request',1,'gh:1','host','execute','owner',1,2,'r30',30)`), tn.Team(), []byte(`{"version":1}`))
		if err != nil {
			t.Fatal(err)
		}
		a, err := capability.Authorize(t.Context(), s.AsOperator(), route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: string(tn.Team()), RunID: "reused"})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := tn.DeleteAuthorizedRun(t.Context(), a, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, kind := range []string{"run.deletion_authorized", "run.deleted"} {
		events, err := tn.BusinessEvents(t.Context(), kind, 10, "")
		if err != nil || len(events) != 2 {
			t.Errorf("%s audit=%+v: %v", kind, events, err)
		}
	}
}
