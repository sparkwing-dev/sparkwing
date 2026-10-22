package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRestoreDrillFencesCredentialsAndScheduleAdmission(t *testing.T) {
	s, _, _ := billingNativeServer(t)
	dir := t.TempDir()
	epoch, err := credentials.LoadOrCreateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(dir, "live.db")
	if _, err := teststore.SQL(t, s.store).ExecContext(t.Context(), `VACUUM INTO ?`, livePath); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	options := []store.Option{
		teststore.WithClock(func() time.Time { return now }),
		store.WithPolicies(func(*store.Store) (store.Policies, error) {
			return store.Policies{Intake: scheduleIntakePolicy{}}, nil
		}),
	}
	live, err := teststore.OpenExisting(livePath, append(options, store.WithCredentialEpoch(epoch))...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := live.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := live.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, token, err := teststore.ProvisionCredential(t.Context(), live, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	liveTenant, err := live.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	schedule, _, err := liveTenant.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "restore-nightly", RepoPath: "https://github.com/acme/repo.git", Pipeline: "nightly", Cron: "* * * * *", Overlap: store.CronOverlapQueue, CatchUp: 24 * time.Hour,
		Where: store.CronWhereController, GitBranch: "main", LockedRef: "0123456789abcdef0123456789abcdef01234567",
		GitHubInstallationID: 7, GitHubRepositoryID: 701, ArmedBy: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, live, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',1,'acme','Organization',1,1)`)
	engineSQL(t, live, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a',?,701,'acme/repo',7,'owner',1)`, schedule.RepositoryKey)
	if status, err := live.AsOperator().RestoreStatus(t.Context()); err != nil || status.Pending {
		t.Fatalf("backup must be normally operating: %+v %v", status, err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: token.Prefix}
	if _, err := live.AsOperator().ResolveAuthority(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	pendingDue := now.Add(time.Minute).Truncate(time.Minute)
	now = pendingDue.Add(time.Second)
	pending, err := liveTenant.AdmitScheduledRun(t.Context(), schedule, pendingDue)
	if err != nil || pending.RunID == "" {
		t.Fatalf("pending scheduled deploy: %+v %v", pending, err)
	}
	if run, err := liveTenant.ReadRun(t.Context(), pending.RunID); err != nil || run.State != store.RunPending || run.CancelRequestedAt != nil {
		t.Fatalf("backup scheduled run: state=%s cancelled=%v err=%v", run.State, run.CancelRequestedAt, err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if _, err := teststore.SQL(t, live).ExecContext(t.Context(), `VACUUM INTO ?`, backupPath); err != nil {
		t.Fatal(err)
	}
	firedAfterBackup := now.Add(time.Minute).Truncate(time.Minute)
	now = firedAfterBackup.Add(time.Second)
	if result, err := liveTenant.AdmitScheduledRun(t.Context(), schedule, firedAfterBackup); err != nil || result.RunID == "" {
		t.Fatalf("live cron admission: %+v %v", result, err)
	}
	if err := live.AsOperator().RevokeAuthority(t.Context(), store.Actor{Authority: ref}, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := live.AsOperator().ResolveAuthority(t.Context(), ref); err == nil {
		t.Fatal("revoked live token accepted")
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(dir, "restored.db")
	if err := os.WriteFile(restoredPath, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	restoredEpoch, err := credentials.RotateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := teststore.OpenExisting(restoredPath, append(options, store.WithCredentialEpoch(restoredEpoch))...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := restored.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.AsOperator().ResolveAuthority(t.Context(), ref); err == nil {
		t.Fatal("backup resurrected revoked token")
	}
	tenant, err := restored.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if run, err := tenant.ReadRun(t.Context(), pending.RunID); err != nil || run.CancelRequestedAt == nil || run.CancelReason != "cancelled by restore; rerun if needed" {
		t.Fatalf("restored pending deploy: cancelled=%v reason=%q err=%v", run.CancelRequestedAt, run.CancelReason, err)
	}
	seed := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if _, err := restored.AsOperator().EnsureBootstrapCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Seed: seed, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}}); err != nil {
		t.Fatal(err)
	}
	launcher, err := restored.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Authority: &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: seed[:store.CredentialPrefixLength]},
		Kind:      capability.ProvisionService, RegistrationID: "service:restore-launcher", Subject: capability.Subject{Kind: capability.SubjectService, ID: "restore-launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: launcher.Capability.Prefix}); err != nil {
		t.Fatalf("fresh launcher was not authorized: %v", err)
	}
	var refused *store.ErrIllegalTransition
	if _, err := restored.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: launcher.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(pending.RunID)}}); !errors.As(err, &refused) || refused.Reason != store.ReasonHolderRights {
		t.Fatalf("restored pending deploy claim did not refuse cancelled work: %v", err)
	}
	if ran, err := restored.AsOperator().RunCronTickLeased(t.Context(), "restore-drill", time.Minute, func(context.Context) error { t.Fatal("restored cron tick ran before acknowledgement"); return nil }); ran || !errors.Is(err, store.ErrRestorePending) {
		t.Fatalf("restored cron hold: ran=%t err=%v", ran, err)
	}
	if result, err := tenant.AdmitScheduledRun(t.Context(), schedule, firedAfterBackup); !errors.Is(err, store.ErrRestorePending) || result.RunID != "" {
		t.Fatalf("restored cron admission before acknowledgement: %+v %v", result, err)
	}
	if err := restored.AsOperator().AcknowledgeRestore(t.Context()); err != nil {
		t.Fatal(err)
	}
	schedule, err = tenant.GetCronSchedule(t.Context(), "restore-nightly")
	if err != nil || schedule.CursorAt.Before(firedAfterBackup) || schedule.NextDueAt != nil {
		t.Fatalf("acknowledgement can replay an already fired instant: %+v %v", schedule, err)
	}
	if result, err := tenant.AdmitScheduledRun(t.Context(), schedule, firedAfterBackup); !errors.Is(err, store.ErrRestorePending) || result.RunID != "" {
		t.Fatalf("acknowledgement admitted an already fired instant: %+v %v", result, err)
	}
	next := firedAfterBackup.Add(time.Minute)
	now = next.Add(time.Second)
	if result, err := tenant.AdmitScheduledRun(t.Context(), schedule, next); err != nil || result.RunID == "" || result.Replayed {
		t.Fatalf("future cron admission after acknowledgement: %+v %v", result, err)
	}
	if fires, err := tenant.ListCronFires(t.Context(), schedule.ID, 0); err != nil || len(fires) != 2 || !fires[0].DueAt.Equal(next) || !fires[1].DueAt.Equal(pendingDue) {
		t.Fatalf("restored cron fired a duplicate instant: %+v %v", fires, err)
	}
}
