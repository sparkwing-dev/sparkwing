package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGitHubDeliveryCompletionReleasesPayload(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_000_000, 0)
	op := s.AsOperator()
	if err := op.EnqueueGitHubDelivery(t.Context(), "delivery", "push", []byte(`{"ref":"main"}`), now); err != nil {
		t.Fatal(err)
	}
	claim, err := op.ClaimGitHubDelivery(t.Context(), now)
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if err := op.FinishGitHubDelivery(t.Context(), *claim, "", now); err != nil {
		t.Fatal(err)
	}
	var state string
	var bodyIsNull bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT state,body IS NULL FROM github_app_deliveries WHERE delivery_id='delivery'`).Scan(&state, &bodyIsNull); err != nil {
		t.Fatal(err)
	}
	if state != "done" || !bodyIsNull {
		t.Fatalf("completed delivery retained payload: state=%q null=%v", state, bodyIsNull)
	}
}

func TestFailedGitHubDeliveryManualProcessingIsTeamScopedAndAudited(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	a := teamAuthorization(t, s, actor, "a", "PATCH", "/api/v1/team")
	now := time.Now()
	op := s.AsOperator()
	for _, id := range []string{"ours", "foreign"} {
		if err := op.EnqueueGitHubDelivery(t.Context(), id, "push", []byte(id), now); err != nil {
			t.Fatal(err)
		}
		claim, err := op.ClaimGitHubDelivery(t.Context(), now)
		if err != nil || claim == nil {
			t.Fatalf("claim: %+v %v", claim, err)
		}
		if err := op.FinishGitHubDelivery(t.Context(), *claim, "processing failed", now); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaExec(t, s.db, `UPDATE github_app_deliveries SET queue_team=CASE delivery_id WHEN 'ours' THEN 'a' ELSE 'b' END`)
	list, err := tenant.ListFailedGitHubDeliveries(t.Context(), a)
	if err != nil || len(list) != 1 || list[0].ID != "ours" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := tenant.ProcessGitHubDeliveryAgain(t.Context(), a, "foreign"); err == nil {
		t.Fatal("processed foreign delivery")
	}
	forged := a
	forged.Target.Team = "b"
	if _, err := tenant.ListFailedGitHubDeliveries(t.Context(), forged); err == nil {
		t.Fatal("listed with foreign authority")
	}
	if err := tenant.ProcessGitHubDeliveryAgain(t.Context(), forged, "ours"); err == nil {
		t.Fatal("processed with foreign authority")
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_delivery_audit BEFORE INSERT ON business_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err := tenant.ProcessGitHubDeliveryAgain(t.Context(), a, "ours"); err == nil {
		t.Fatal("processed without audit")
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_delivery_audit`)
	if err := tenant.ProcessGitHubDeliveryAgain(t.Context(), a, "ours"); err != nil {
		t.Fatal(err)
	}
	claim, err := op.ClaimGitHubDelivery(t.Context(), now)
	if err != nil || claim == nil || claim.ID != "ours" {
		t.Fatalf("manual claim: %+v %v", claim, err)
	}
	events, err := tenant.BusinessEvents(t.Context(), "github.delivery_reprocessed")
	if err != nil || len(events) != 1 || events[0].SubjectID != "ours" {
		t.Fatalf("audit: %+v %v", events, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='owner'`)
	if _, err := tenant.ListFailedGitHubDeliveries(t.Context(), a); err == nil {
		t.Fatal("revoked admin listed failures")
	}
	if err := tenant.ProcessGitHubDeliveryAgain(t.Context(), a, "ours"); err == nil {
		t.Fatal("revoked admin processed delivery")
	}
}

func TestFailedGitHubDeliveryCannotMutateReplacementConnection(t *testing.T) {
	for _, replacement := range []string{"move", "move-back", "reconnect"} {
		for _, boundary := range []string{"process-again", "mutation"} {
			for _, effect := range []string{"suspend", "deleted"} {
				t.Run(replacement+"/"+boundary+"/"+effect, func(t *testing.T) {
					s := authorityStore(t)
					actor := authorityMember(t, s, "owner", RoleOwner)
					tenant := &Tenant{s: s, team: "a"}
					a := teamAuthorization(t, s, actor, "a", "PATCH", "/api/v1/team")
					now := time.Now()
					op, ctx := s.AsOperator(), t.Context()
					bind := func(team Team) {
						t.Helper()
						if _, err := (&Tenant{s: s, team: team}).TestOnlySeedGitHubInstallation(ctx, GitHubAppInstallation{InstallationID: 7, AccountID: 7, AccountLogin: "org", AccountType: "Organization"}, now); err != nil {
							t.Fatal(err)
						}
					}
					replace := func() Team {
						t.Helper()
						if _, err := op.UnbindGitHubAppInstallation(ctx, 7); err != nil {
							t.Fatal(err)
						}
						if replacement != "reconnect" {
							bind("b")
							if replacement == "move" {
								return "b"
							}
							if _, err := op.UnbindGitHubAppInstallation(ctx, 7); err != nil {
								t.Fatal(err)
							}
						}
						bind("a")
						return "a"
					}
					bind("a")
					if err := op.EnqueueGitHubDelivery(ctx, "old", "installation", []byte(`{"installation":{"id":7}}`), now); err != nil {
						t.Fatal(err)
					}
					claim, err := op.ClaimGitHubDelivery(ctx, now)
					if err != nil || claim == nil {
						t.Fatalf("claim: %+v %v", claim, err)
					}
					if err := op.FinishGitHubDelivery(ctx, *claim, "failed before mutation", now); err != nil {
						t.Fatal(err)
					}
					var current Team
					if boundary == "process-again" {
						current = replace()
						if err := tenant.ProcessGitHubDeliveryAgain(ctx, a, "old"); err == nil {
							t.Fatal("old delivery requeued against replacement connection")
						}
						if replay, err := op.ClaimGitHubDelivery(ctx, now); err != nil || replay != nil {
							t.Fatalf("refused delivery became claimable: %+v %v", replay, err)
						}
					} else {
						if err := tenant.ProcessGitHubDeliveryAgain(ctx, a, "old"); err != nil {
							t.Fatal(err)
						}
						replay, err := op.ClaimGitHubDelivery(ctx, now)
						if err != nil || replay == nil {
							t.Fatalf("replay: %+v %v", replay, err)
						}
						current = replace()
						if effect == "suspend" {
							err = op.SetGitHubAppInstallationSuspended(replay.Context(ctx), 7, true)
						} else {
							_, err = op.UnbindGitHubAppInstallation(replay.Context(ctx), 7)
						}
						if err == nil {
							t.Fatal("old delivery mutated replacement connection")
						}
					}
					installation, err := op.GitHubAppInstallationTeam(ctx, 7)
					if err != nil || installation.Team != current || installation.Suspended {
						t.Fatalf("replacement changed: %+v %v", installation, err)
					}
					rewriteSchemaCount(t, s.db, "github_app_delivery_effects", 0)
					events, err := tenant.BusinessEvents(ctx, "github.delivery_reprocessed")
					if err != nil || boundary == "process-again" && len(events) != 0 {
						t.Fatalf("refused requeue was audited as successful: %+v %v", events, err)
					}
				})
			}
		}
	}
}
