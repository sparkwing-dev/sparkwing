package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestGitHubInstallationWithdrawalReplayKeepsOriginalTargets(t *testing.T) {
	for _, fallbackFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(fallbackFirst), func(t *testing.T) {
			s, tenant, in := appAdmissionFixture(t, true, "fixture")
			defer s.Close()
			ctx := t.Context()
			arm := func(repository int64) CronSchedule {
				t.Helper()
				row := CronSchedule{ID: fmt.Sprintf("cron-%d", repository), RepoPath: fmt.Sprintf("https://github.com/acme/repo-%d", repository), Pipeline: "ci", Name: "nightly", Cron: "0 * * * *", TZ: "UTC", Where: CronWhereController, GitHubInstallationID: 7, GitHubRepositoryID: repository}
				stored, _, err := tenant.ArmCronSchedule(ctx, row)
				if err != nil {
					t.Fatal(err)
				}
				return stored
			}
			original := arm(701)
			op, now := s.AsOperator(), in.Request.Git.App.Value.VerifiedAt
			if err := op.EnqueueGitHubDelivery(ctx, "withdraw", "installation_repositories", []byte(`{"installation":{"id":7},"action":"removed"}`), now); err != nil {
				t.Fatal(err)
			}
			delivery, err := op.ClaimGitHubDelivery(ctx, now)
			if err != nil || delivery == nil {
				t.Fatalf("claim: %v %v", delivery, err)
			}
			if fallbackFirst {
				if err := tenant.WithdrawGitHubInstallationRepositories(delivery.Context(ctx), 7, []int64{701}); err != nil {
					t.Fatal(err)
				}
			} else if err := tenant.ReconcileGitHubInstallationRepositories(delivery.Context(ctx), 7, nil); err != nil {
				t.Fatal(err)
			}
			if err := op.FinishGitHubDelivery(ctx, *delivery, "listing outage", now); err != nil {
				t.Fatal(err)
			}
			later := arm(702)
			rewriteSchemaExec(t, s.db, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a','gh:702',702,'acme/repo-702',7,'owner',?)`, now.Add(time.Second).UnixNano())
			rewriteSchemaExec(t, s.db, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id='withdraw'`)
			delivery, err = op.ClaimGitHubDelivery(ctx, now.Add(time.Second))
			if err != nil || delivery == nil {
				t.Fatalf("reclaim: %v %v", delivery, err)
			}
			if err := tenant.ReconcileGitHubInstallationRepositories(delivery.Context(ctx), 7, []int64{701}); err != nil {
				t.Fatal(err)
			}
			// A changed listing or an outage keeps the delivery's committed targets.
			if err := tenant.WithdrawGitHubInstallationRepositories(delivery.Context(ctx), 7, []int64{702}); err != nil {
				t.Fatal(err)
			}
			if err := tenant.ReconcileGitHubInstallationRepositories(delivery.Context(ctx), 7, nil); err != nil {
				t.Fatal(err)
			}
			for _, want := range []struct {
				id       string
				declared bool
			}{{original.ID, false}, {later.ID, true}} {
				row, err := tenant.GetCronSchedule(ctx, want.id)
				if err != nil || row.Declared != want.declared {
					t.Fatalf("schedule %s declared=%t, want %t: %v", want.id, row.Declared, want.declared, err)
				}
			}
			if _, err := tenant.GitHubAppAutomation(ctx, 7, 702); err != nil {
				t.Fatalf("replay withdrew later consent: %v", err)
			}
			var result githubInstallationWithdrawal
			var raw string
			if err := s.db.QueryRow(`SELECT result FROM github_app_delivery_effects WHERE effect LIKE 'withdraw-installation:%'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &result); err != nil || !reflect.DeepEqual(result.Repositories, []int64{701}) || result.Count != 1 {
				t.Fatalf("receipt did not keep original targets and result: %+v %v", result, err)
			}
		})
	}
}

func publicationRows(in GitHubAppAdmission) ([]CronSchedule, []runspec.Request) {
	row := CronSchedule{RepoPath: in.Request.Source.Location.RemoteURL, Pipeline: "ci", Name: "nightly", Cron: "0 * * * *", TZ: "UTC", Overlap: CronOverlapSkip, Where: CronWhereController, GitBranch: "main", GitHubInstallationID: 7, GitHubRepositoryID: 701}
	return []CronSchedule{row}, []runspec.Request{in.Request}
}

func TestGitHubPublicationGenerationSimulation(t *testing.T) {
	// Two independent preparations model separate workers; source fetch happens between prepare and publish.
	random := rand.New(rand.NewPCG(44, 35))
	for trial := 0; trial < 12; trial++ {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			s, tenant, in := appAdmissionFixture(t, true, "fixture")
			defer s.Close()
			first := prepareAppPublication(t, tenant, 7, 701)
			second := prepareAppPublication(t, tenant, 7, 701)
			rows, requests := publicationRows(in)
			winner, loser := first, second
			if random.IntN(2) == 1 {
				winner, loser = second, first
			}
			ids, _, err := tenant.ReplaceGitHubAppSchedules(t.Context(), winner, in.Request.Source.Location.RemoteURL, rows, requests)
			if err != nil || len(ids) != 1 {
				t.Fatalf("first publication: %v %v", ids, err)
			}
			if _, _, err := tenant.ReplaceGitHubAppSchedules(t.Context(), loser, in.Request.Source.Location.RemoteURL, nil, nil); !errors.Is(err, ErrGitHubPublicationStale) {
				t.Fatalf("stale snapshot overwrote winner: %v", err)
			}
			prepared := prepareAppPublication(t, tenant, 7, 701)
			if _, err := tenant.WithdrawGitHubCronRepository(t.Context(), 7, 701); err != nil {
				t.Fatal(err)
			}
			if _, _, err := tenant.ReplaceGitHubAppSchedules(t.Context(), prepared, in.Request.Source.Location.RemoteURL, rows, requests); !errors.Is(err, ErrGitHubPublicationStale) {
				t.Fatalf("withdrawal did not fence prepared publication: %v", err)
			}
		})
	}
}

func TestGitHubDeliveryPublicationReceiptSurvivesWithdrawal(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	ctx := t.Context()
	now := in.Request.Git.App.Value.VerifiedAt
	body := []byte(`{"installation":{"id":7},"after":"publication"}`)
	op := s.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "publication", "push", body, now); err != nil {
		t.Fatal(err)
	}
	delivery, err := op.ClaimGitHubDelivery(ctx, now)
	if err != nil || delivery == nil {
		t.Fatalf("claim: %v %v", delivery, err)
	}
	dc := delivery.Context(ctx)
	publication, replay, err := tenant.PrepareGitHubAppPublication(dc, 7, 701)
	if err != nil || replay {
		t.Fatalf("prepare: %v %v", replay, err)
	}
	rows, requests := publicationRows(in)
	ids, withdrawn, err := tenant.ReplaceGitHubAppSchedules(dc, publication, in.Request.Source.Location.RemoteURL, rows, requests)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.FinishGitHubDelivery(ctx, *delivery, "failed after publication", now); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.WithdrawGitHubCronRepository(ctx, 7, 701); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id='publication'`)
	delivery, err = op.ClaimGitHubDelivery(ctx, now.Add(time.Second))
	if err != nil || delivery == nil {
		t.Fatalf("reclaim: %v %v", delivery, err)
	}
	dc = delivery.Context(ctx)
	publication, replay, err = tenant.PrepareGitHubAppPublication(dc, 7, 701)
	if err != nil || !replay {
		t.Fatalf("receipt lost: %v %v", replay, err)
	}
	again, againWithdrawn, err := tenant.ReplaceGitHubAppSchedules(dc, publication, in.Request.Source.Location.RemoteURL, rows, requests)
	if err != nil || !reflect.DeepEqual(ids, again) || !reflect.DeepEqual(withdrawn, againWithdrawn) {
		t.Fatalf("stored result lost: %v %v %v", again, againWithdrawn, err)
	}
	row, err := tenant.GetCronSchedule(ctx, ids[0])
	if err != nil || row.Declared {
		t.Fatalf("old replay restored withdrawn schedule: %+v %v", row, err)
	}
}

func TestGitHubDeliveryTargetedAdmissionsReplayBeforeSource(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	ctx := t.Context()
	now := in.Request.Git.App.Value.VerifiedAt
	body := []byte(`{"installation":{"id":7},"after":"admission"}`)
	op := s.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "admission", "push", body, now); err != nil {
		t.Fatal(err)
	}
	delivery, err := op.ClaimGitHubDelivery(ctx, now)
	if err != nil || delivery == nil {
		t.Fatalf("claim: %v %v", delivery, err)
	}
	in.Request.Git.App.Value.DeliveryKey = fmt.Sprintf("%x", sha256.Sum256(body))
	var results []AdmissionResult
	for _, pipeline := range []string{"ci", "deploy"} {
		in.Request.Pipeline = pipeline
		result, err := tenant.AdmitGitHubAppRun(delivery.Context(ctx), in)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	s.intakePolicy = nil
	// A changed branch and unavailable provider must not affect committed delivery results.
	in.Request.Git.SHA = "ffffffffffffffffffffffffffffffffffffffff"
	in.Request.Git.App.Value.SHA = in.Request.Git.SHA
	for i, pipeline := range []string{"ci", "deploy"} {
		in.Request.Pipeline = pipeline
		result, err := tenant.AdmitGitHubAppRun(delivery.Context(ctx), in)
		if err != nil || result.RunID != results[i].RunID {
			t.Fatalf("%s replay: %+v %v", pipeline, result, err)
		}
	}
	if results[0].RunID == results[1].RunID {
		t.Fatal("target pipelines shared admission receipt")
	}
}

func TestGitHubDeliveryRejectsEmptyEffect(t *testing.T) {
	s, _, _ := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := githubDeliveryResultTx(t.Context(), tx, "", func() (int, error) { t.Fatal("empty effect applied"); return 0, nil })
		return err
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty effect: %v", err)
	}
}

func TestGitHubRepositoryWithdrawalFencesFirstPublication(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	prepared := prepareAppPublication(t, tenant, 7, 701)
	if _, err := tenant.WithdrawGitHubCronRepository(t.Context(), 7, 701); err != nil {
		t.Fatal(err)
	}
	rows, requests := publicationRows(in)
	if _, _, err := tenant.ReplaceGitHubAppSchedules(t.Context(), prepared, in.Request.Source.Location.RemoteURL, rows, requests); !errors.Is(err, ErrGitHubPublicationStale) {
		t.Fatalf("repository withdrawal admitted previously prepared source: %v", err)
	}
}

func TestGitHubDeliveryUnbindReturnsStoredTeam(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	ctx := t.Context()
	op, now := s.AsOperator(), in.Request.Git.App.Value.VerifiedAt
	if err := op.EnqueueGitHubDelivery(ctx, "unbind", "installation", []byte(`{"installation":{"id":7},"action":"deleted"}`), now); err != nil {
		t.Fatal(err)
	}
	delivery, err := op.ClaimGitHubDelivery(ctx, now)
	if err != nil || delivery == nil {
		t.Fatalf("claim: %v %v", delivery, err)
	}
	for i := 0; i < 2; i++ {
		team, err := op.UnbindGitHubAppInstallation(delivery.Context(ctx), 7)
		if err != nil || team != tenant.team {
			t.Fatalf("unbind %d lost result: %s %v", i, team, err)
		}
	}
}

func TestGitHubUnappliedPublicationCannotAcquireLaterConsent(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	ctx := t.Context()
	op, now := s.AsOperator(), in.Request.Git.App.Value.VerifiedAt
	if err := op.EnqueueGitHubDelivery(ctx, "before-consent", "push", []byte(`{"installation":{"id":7},"after":"before-consent"}`), now); err != nil {
		t.Fatal(err)
	}
	delivery, err := op.ClaimGitHubDelivery(ctx, now)
	if err != nil || delivery == nil {
		t.Fatalf("claim: %v %v", delivery, err)
	}
	if err := tenant.WithdrawGitHubAppAutomation(ctx, 7, 701); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a','gh:701',701,'acme/app',7,'owner',?)`, now.Add(time.Second).UnixNano())
	if _, _, err := tenant.PrepareGitHubAppPublication(delivery.Context(ctx), 7, 701); !errors.Is(err, ErrGitHubAppAutomationRevoked) {
		t.Fatalf("old delivery gained later consent: %v", err)
	}
}

func TestGitHubDeliveryLosingConnectionDuringPreparationIsRevoked(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "fixture")
	defer s.Close()
	ctx := t.Context()
	op, now := s.AsOperator(), in.Request.Git.App.Value.VerifiedAt
	body := []byte(`{"installation":{"id":7},"after":"unbound-during-fetch"}`)
	if err := op.EnqueueGitHubDelivery(ctx, "unbound-during-fetch", "push", body, now); err != nil {
		t.Fatal(err)
	}
	delivery, err := op.ClaimGitHubDelivery(ctx, now)
	if err != nil || delivery == nil {
		t.Fatalf("claim: %v %v", delivery, err)
	}
	dc := delivery.Context(ctx)
	publication, _, err := tenant.PrepareGitHubAppPublication(dc, 7, 701)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.UnbindGitHubAppInstallation(ctx, 7); err != nil {
		t.Fatal(err)
	}
	in.Request.Git.App.Value.DeliveryKey = fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := tenant.AdmitGitHubAppRun(dc, in); !errors.Is(err, ErrGitHubAppAutomationRevoked) {
		t.Fatalf("connection lost during source fetch was not a revoked admission: %v", err)
	}
	rows, requests := publicationRows(in)
	if _, _, err := tenant.ReplaceGitHubAppSchedules(dc, publication, in.Request.Source.Location.RemoteURL, rows, requests); !errors.Is(err, ErrGitHubAppAutomationRevoked) {
		t.Fatalf("connection lost during source fetch was not a revoked publication: %v", err)
	}
	rewriteSchemaCount(t, s.db, "github_app_delivery_effects", 0)
}
