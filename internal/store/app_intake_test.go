package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func appAdmissionFixture(t *testing.T, automatic bool, digest string) (*Store, *Tenant, GitHubAppAdmission) {
	t.Helper()
	s, tenant, auth, now := githubMutationFixture(t)
	installation, err := tenant.GitHubAppInstallation(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	in := GitHubAppAdmission{Installation: installation, Event: "push", Request: runspec.Request{
		Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Origin: runspec.OriginApp, Dispatch: runspec.DispatchHost,
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:701", Display: "acme/app"}, Ref: "refs/heads/main", SHA: "0123456789abcdef0123456789abcdef01234567", App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "push", DeliveryKey: digest, InstallationID: 7, RepositoryID: 701, Ref: "refs/heads/main", SHA: "0123456789abcdef0123456789abcdef01234567", DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: *now}}},
		Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567", Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}},
	}}
	if automatic {
		if err := tenant.PutAuthorizedGitHubAutomation(t.Context(), auth, GitHubAppAutomation{RepositoryID: 701, Repository: "acme/app", InstallationID: 7}); err != nil {
			t.Fatal(err)
		}
		grant, err := tenant.GitHubAppAutomation(t.Context(), 7, 701)
		if err != nil {
			t.Fatal(err)
		}
		in.Automation = &grant
	} else {
		sub, err := tenant.PutAuthorizedGitHubTrigger(t.Context(), auth, GitHubAppTrigger{RepositoryID: 701, Repository: "acme/app", InstallationID: 7, Pipeline: "ci", Push: true})
		if err != nil {
			t.Fatal(err)
		}
		in.Subscription = &sub
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_deliveries(digest,delivery_id,received_at,event) VALUES (?,?,?,?)`, digest, "delivery", now.UnixNano(), "push")
	s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, resolver capability.AuthorityResolver, request IntakeRequest) (IntakePolicyFacts, error) {
		delivery, err := VerifiedAppIntake(ctx, resolver, request)
		location := request.Request.Source.Location
		return IntakePolicyFacts{Delivery: &delivery, SourceLocation: &location, LogRetentionClass: "r30", LogRetentionDays: 30}, err
	})
	return s, tenant, in
}

func TestAppAdmissionRequiresCurrentOriginProperty(t *testing.T) {
	property := func(automatic bool, mode uint8, salt uint64) bool {
		s, tenant, in := appAdmissionFixture(t, automatic, fmt.Sprintf("digest-%x", salt))
		defer s.Close()
		valid, err := tenant.AdmitGitHubAppRun(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		if replay, err := tenant.AdmitGitHubAppRun(t.Context(), in); err != nil || !replay.Replayed || replay.RunID != valid.RunID {
			t.Fatalf("replay=%+v err=%v", replay, err)
		}
		switch mode % 8 {
		case 0:
			rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET suspended=1`)
		case 1:
			rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET team='b'`)
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET created_at=created_at+1`)
		case 3:
			rewriteSchemaExec(t, s.db, `DELETE FROM github_app_automation`)
			rewriteSchemaExec(t, s.db, `DELETE FROM github_app_triggers`)
		case 4:
			rewriteSchemaExec(t, s.db, `UPDATE github_app_automation SET enabled_at=enabled_at+1`)
			rewriteSchemaExec(t, s.db, `UPDATE github_app_triggers SET created_at=created_at+1`)
		case 5:
			rewriteSchemaExec(t, s.db, `UPDATE github_app_deliveries SET event='pull_request'`)
		case 6:
			in.Request.Team = "b"
		case 7:
			in.Request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Fork: true, BaseRepo: "gh:701", HeadRepo: "gh:999"}}
		}
		result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
		if err == nil || result.RunID != "" || result.OwnerUnit != nil {
			t.Logf("refused=%+v err=%v", result, err)
			return false
		}
		rewriteSchemaCount(t, s.db, "runs", 1)
		rewriteSchemaCount(t, s.db, "triggers", 1)
		var number int
		if err := s.db.QueryRow(`SELECT next_run_number FROM teams WHERE name='a'`).Scan(&number); err != nil {
			t.Fatal(err)
		}
		return number == 2
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

func TestAppAdmissionRevokedInstallationInClaim(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, revoke := range []string{"suspend", "unbind"} {
			t.Run(fmt.Sprintf("automatic=%t/%s", automatic, revoke), func(t *testing.T) {
				s, tenant, in := appAdmissionFixture(t, automatic, "fixture")
				defer s.Close()
				ctx, now, op := t.Context(), s.clock(), s.AsOperator()
				body := []byte(`{"installation":{"id":7}}`)
				if err := op.EnqueueGitHubDelivery(ctx, "queued", "push", body, now); err != nil {
					t.Fatal(err)
				}
				claim, err := op.ClaimGitHubDelivery(ctx, now)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				in.Request.Git.App.Value.DeliveryKey = fmt.Sprintf("%x", sha256.Sum256(body))
				if revoke == "unbind" {
					_, err = op.UnbindGitHubAppInstallation(ctx, 7)
				} else {
					err = op.SetGitHubAppInstallationSuspended(ctx, 7, true, now)
				}
				if err != nil {
					t.Fatal(err)
				}
				result, err := tenant.AdmitGitHubAppRun(claim.Context(ctx), in)
				if !errors.Is(err, ErrGitHubAppAutomationRevoked) || result.RunID != "" {
					t.Fatalf("revoked admission: run=%q error=%v", result.RunID, err)
				}
				rewriteSchemaCount(t, s.db, "runs", 0)
				rewriteSchemaCount(t, s.db, "triggers", 0)
			})
		}
	}
}

func TestAppAdmissionRefusesUntypedOriginAndRollsBack(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "signed")
	defer s.Close()
	if _, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: in.authority(), Request: in.Request, VerifiedDeliveryID: "signed"}); err == nil {
		t.Fatal("ordinary intake manufactured a trusted origin")
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER fail_run_effect BEFORE INSERT ON run_effects BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if _, err := tenant.AdmitGitHubAppRun(t.Context(), in); err == nil {
		t.Fatal("failed effect committed intake")
	}
	rewriteSchemaCount(t, s.db, "runs", 0)
	rewriteSchemaCount(t, s.db, "triggers", 0)
	var boundTeam string
	if err := s.db.QueryRow(`SELECT admitted_team FROM github_app_deliveries WHERE digest='signed'`).Scan(&boundTeam); err != nil || boundTeam != "" {
		t.Fatalf("failed intake bound replay team=%q err=%v", boundTeam, err)
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER fail_run_effect`)
	first, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil || first.Number != 1 || !first.Request.Request.Git.App.Value.DefaultBranchPush || !first.Request.Request.Git.DefaultBranch.Set || first.Request.Request.Source.Location != in.Request.Source.Location {
		t.Fatalf("sealed=%+v err=%v", first, err)
	}
}

func TestAppFullRefRetainsExactDefaultBranchDataTrustProperty(t *testing.T) {
	property := func(suffix uint32, mode uint8) bool {
		s, records, intent := blobRecordFixture(t)
		defer s.Close()
		tenant := records.tenant
		var raw []byte
		if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id=?`, intent.Identity.RunID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var request runspec.SealedRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		branch := fmt.Sprintf("release-%x", suffix)
		ref := "refs/heads/" + branch
		git := &request.Request.Git
		git.Ref = ref
		if !runspec.IsCommitID(git.SHA) {
			git.SHA = fixtureFeatureSHA
		}
		verifiedAt := request.AdmittedAt
		if verifiedAt.IsZero() {
			verifiedAt = time.Unix(1, 0).UTC()
		}
		git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "signed", Ref: ref, SHA: git.SHA, DefaultBranch: branch, DefaultBranchPush: true, VerifiedAt: verifiedAt}}
		switch mode % 5 {
		case 1:
			git.Ref = "refs/tags/" + branch
			git.App.Value.Ref = git.Ref
		case 2:
			git.App.Value.SHA += "foreign"
		case 3:
			git.App.Value.DefaultBranchPush = false
		case 4:
			git.App.Set = false
		}
		// Intake seals verified provenance only from App facts it could verify.
		request.Provenance = assertedProvenance(t, "ci", git.Ref, "")
		if p, err := runspec.AppProvenance(git.App.Value, request.Request.PullRequest, "ci"); git.App.Set && err == nil {
			request.Provenance = p
		}
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id=?`, raw, intent.Identity.RunID)
		got, err := tenant.ResolveBlobAuthority(t.Context(), blob.Authority{CapabilityRef: intent.Authority.CapabilityRef}, capability.DataRead)
		if err != nil {
			t.Fatal(err)
		}
		if mode%5 == 0 {
			return got.Ref.DefaultBranchVerified && got.Ref.DefaultBranch == ref && got.Ref.Scope.Ref == ref && got.Ref.Scope.Verified
		}
		return !got.Ref.DefaultBranchVerified
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
}

func TestAppPartialDeliveryCannotMoveTeamsProperty(t *testing.T) {
	property := func(automatic bool, suffix uint32) bool {
		s, tenant, in := appAdmissionFixture(t, automatic, fmt.Sprintf("partial-%x", suffix))
		defer s.Close()
		if _, err := tenant.AdmitGitHubAppRun(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET team='b',created_at=created_at+1`)
		rewriteSchemaExec(t, s.db, `UPDATE github_app_automation SET team='b'`)
		rewriteSchemaExec(t, s.db, `UPDATE github_app_triggers SET team='b',pipeline='other'`)
		in.Installation.Team = "b"
		in.Installation.CreatedAt = in.Installation.CreatedAt.Add(time.Nanosecond)
		in.Request.Team, in.Request.Pipeline = "b", "other"
		if in.Subscription != nil {
			in.Subscription.Team, in.Subscription.Pipeline = "b", "other"
		}
		other := &Tenant{s: s, team: "b"}
		result, err := other.AdmitGitHubAppRun(t.Context(), in)
		rewriteSchemaCount(t, s.db, "runs", 1)
		rewriteSchemaCount(t, s.db, "triggers", 1)
		return err != nil && result.RunID == ""
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestAppAdmissionOIDCUsesSealedEventAfterReplayExpiry(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "oidc-event")
	defer s.Close()
	admitted, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM github_app_deliveries`)
	run, err := tenant.ReadRun(t.Context(), admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	trigger, repository, _, _ := run.Request.Provenance.OIDC()
	if trigger != "push" || repository != 701 {
		t.Fatalf("sealed event trigger=%s repository=%d", trigger, repository)
	}
}

func TestAppAdmissionFencesDeliveryLeaseAndOriginalTeam(t *testing.T) {
	for _, state := range []string{"current", "moved", "expired"} {
		t.Run(state, func(t *testing.T) {
			s, tenant, in := appAdmissionFixture(t, true, "fixture")
			defer s.Close()
			ctx, now := t.Context(), s.clock()
			body := []byte(`{"installation":{"id":7}}`)
			op := s.AsOperator()
			if err := op.EnqueueGitHubDelivery(ctx, "queued", "push", body, now); err != nil {
				t.Fatal(err)
			}
			first, err := op.ClaimGitHubDelivery(ctx, now)
			if err != nil || first == nil {
				t.Fatalf("claim: %+v %v", first, err)
			}
			if err := op.FinishGitHubDelivery(ctx, *first, "failed before admission", now); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id='queued'`)
			claim, err := op.ClaimGitHubDelivery(ctx, now)
			if err != nil || claim == nil {
				t.Fatalf("manual claim: %+v %v", claim, err)
			}
			in.Request.Git.App.Value.DeliveryKey = fmt.Sprintf("%x", sha256.Sum256(body))
			switch state {
			case "moved":
				rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET team='b' WHERE installation_id=7`)
				rewriteSchemaExec(t, s.db, `UPDATE github_app_automation SET team='b' WHERE installation_id=7`)
				in.Installation.Team, in.Request.Team = "b", "b"
				tenant = &Tenant{s: s, team: "b"}
			case "expired":
				s.clock = func() time.Time { return now.Add(3 * time.Minute) }
			}
			remote := in.Request.Source.Location.RemoteURL
			schedule := CronSchedule{Team: tenant.team, RepoPath: remote, Pipeline: "ci", Name: "nightly", Cron: "0 0 * * *", TZ: "UTC", Overlap: CronOverlapSkip, Where: CronWhereController, GitBranch: "main", GitHubInstallationID: 7, GitHubRepositoryID: 701}
			ids, _, err := tenant.ReplaceGitHubAppSchedules(ctx, 7, 701, remote, []CronSchedule{schedule}, []runspec.Request{in.Request})
			if err != nil || len(ids) != 1 {
				t.Fatalf("arm current owner's schedule: %v %v", ids, err)
			}
			_, _, err = tenant.ReplaceGitHubAppSchedules(claim.Context(ctx), 7, 701, remote, nil, nil)
			if state == "current" && err != nil || state != "current" && err == nil {
				t.Fatalf("%s claim schedule withdrawal: %v", state, err)
			}
			row, err := tenant.GetCronSchedule(ctx, ids[0])
			if err != nil || row.Declared != (state != "current") {
				t.Fatalf("%s schedule changed: %+v %v", state, row, err)
			}

			result, err := tenant.AdmitGitHubAppRun(claim.Context(ctx), in)
			if state == "current" {
				if err != nil || result.RunID == "" {
					t.Fatalf("current claim refused: %+v %v", result, err)
				}
			} else {
				if err == nil || result.RunID != "" {
					t.Fatalf("%s claim admitted work: %s %v", state, result.RunID, err)
				}
				rewriteSchemaCount(t, s.db, "runs", 0)
				rewriteSchemaCount(t, s.db, "triggers", 0)
			}
		})
	}
}
