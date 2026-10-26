package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func actionsEnrollment(t *testing.T) (*Tenant, capability.Authorized, GitHubRunnerBinding) {
	t.Helper()
	tenant, a := credentialMaintenanceFixture(t)
	row, err := tenant.RegisterGitHubRunner(t.Context(), a, GitHubRunnerBinding{RepositoryID: 42, RepositoryOwnerID: 7, Repository: "Acme/Widgets"})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, a, row
}

func actionsSpec(t *testing.T, s *Store, team Team, run string) capability.ProvisionSpec {
	t.Helper()
	now, err := s.databaseNow(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return capability.ProvisionSpec{
		Kind: capability.ProvisionActions, Team: string(team), RegistrationID: "github:42:7",
		Subject:      capability.Subject{Kind: capability.SubjectMachine, ID: "github:42:7:" + run + ":1"},
		Grants:       []capability.Grant{capability.NodesClaim, capability.TriggersClaim},
		Binding:      capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:42", Repository: "acme/repo", Ref: "refs/heads/main", SHA: "commit", WorkflowRunID: run, WorkflowRunAttempt: "1", WorkflowRef: "Acme/Widgets/.github/workflows/sparkwing.yaml@refs/heads/main"}},
		Repositories: []string{"gh:42"}, ExpiresAt: new(now.Add(time.Hour)),
	}
}

func TestGitHubEnrollmentPreservesIdentityAndStoreClock(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	now := time.Unix(1700000000, 123456789).UTC()
	tenant.s.clock = func() time.Time { return now }
	row, err := tenant.RegisterGitHubRunner(t.Context(), a, GitHubRunnerBinding{RepositoryID: 42, RepositoryOwnerID: 7, Repository: " Acme/Widgets "})
	if err != nil || row.CreatedBy != "owner" || row.Repository != "Acme/Widgets" || !row.CreatedAt.Equal(now) {
		t.Fatalf("register: %+v %v", row, err)
	}
	if _, err := tenant.RegisterGitHubRunner(t.Context(), a, row); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("duplicate registration: %v", err)
	}
	rows, err := tenant.GitHubRunnerBindings(t.Context())
	if err != nil || len(rows) != 1 || rows[0] != row {
		t.Fatalf("list: %+v %v", rows, err)
	}
	got, err := tenant.GitHubRunnerBinding(t.Context(), 42)
	if err != nil || got != row {
		t.Fatalf("read: %+v %v", got, err)
	}
	var key string
	if err := tenant.s.db.QueryRow(`SELECT repo_key FROM github_runner_bindings WHERE team='a' AND repository_id=42`).Scan(&key); err != nil || key != "gh:42" {
		t.Fatalf("repository identity: %s %v", key, err)
	}
	foreign := &Tenant{s: tenant.s, team: "b"}
	if _, err := foreign.GitHubRunnerBinding(t.Context(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign binding: %v", err)
	}
}

func TestGitHubEnrollmentRefusesInvalidOrForgedMetadata(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	for _, row := range []GitHubRunnerBinding{
		{RepositoryID: 1, RepositoryOwnerID: 1, Repository: "no-slash"},
		{RepositoryID: 0, RepositoryOwnerID: 1, Repository: "a/b"},
		{RepositoryID: 1, RepositoryOwnerID: 0, Repository: "a/b"},
		{RepositoryID: 1, RepositoryOwnerID: 1, Repository: "a/b c"},
		{RepositoryID: 1, RepositoryOwnerID: 1, Repository: "a/b", Team: "b"},
		{RepositoryID: 1, RepositoryOwnerID: 1, Repository: "a/b", CreatedBy: "other"},
	} {
		if got, err := tenant.RegisterGitHubRunner(t.Context(), a, row); err == nil || got.RepositoryID != 0 {
			t.Fatalf("invalid enrollment: %+v %v", got, err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "github_runner_bindings", 0)
	if err := tenant.RevokeGitHubRunner(t.Context(), a, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown revocation: %v", err)
	}
}

func TestGitHubEnrollmentAuthorityProperty(t *testing.T) {
	tenant, a, row := actionsEnrollment(t)
	random := rand.New(rand.NewPCG(379, 121))
	for i := range 96 {
		bad := a
		target := *tenant
		rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='owner' WHERE account_id='owner'`)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE sessions SET revoked_at=NULL WHERE hash='owner'`)
		input := GitHubRunnerBinding{RepositoryID: int64(i + 100), RepositoryOwnerID: 7, Repository: "acme/new"}
		switch random.IntN(8) {
		case 0:
			bad.Target.Team = "b"
		case 1:
			target.team = "b"
		case 2:
			bad.Access.Grants = []capability.Grant{capability.RunsWrite}
		case 3:
			bad.Access.Credential = capability.CredentialClaim
		case 4:
			bad.Access.Liveness = capability.LiveSensitive
		case 5:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='reader' WHERE account_id='owner'`)
		case 6:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE sessions SET revoked_at=1 WHERE hash='owner'`)
		case 7:
			bad.Principal.Authority.ID = fmt.Sprintf("absent-%d", random.Uint64())
		}
		if out, err := target.RegisterGitHubRunner(t.Context(), bad, input); err == nil || out.RepositoryID != 0 {
			t.Fatalf("invalid authority registered: %+v %v", out, err)
		}
		if err := target.RevokeGitHubRunner(t.Context(), bad, row.RepositoryID); err == nil {
			t.Fatalf("invalid authority revoked enrollment: %d", i)
		}
		rewriteSchemaCount(t, tenant.s.db, "github_runner_bindings", 1)
	}
}

func TestGitHubActionsRequiresCurrentEnrollmentProperty(t *testing.T) {
	tenant, _, _ := actionsEnrollment(t)
	random := rand.New(rand.NewPCG(921, 661))
	for i := range 96 {
		spec := actionsSpec(t, tenant.s, tenant.team, fmt.Sprint(i))
		switch random.IntN(9) {
		case 0:
			spec.Team = "b"
		case 1:
			spec.RegistrationID = "github:42:8"
		case 2:
			spec.Subject.ID = "github:42:8:" + spec.Binding.Push.WorkflowRunID
		case 3:
			spec.Binding.Push.RepositoryKey, spec.Repositories = "gh:43", []string{"gh:43"}
		case 4:
			spec.Metered = true
		case 5:
			spec.ReservedPools = []string{"cloud"}
		case 6:
			spec.Grants = append(spec.Grants, capability.TeamAdmin)
		case 7:
			spec.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:42"}
		case 8:
			spec.ExpiresAt = new(tenant.s.now().Add(-time.Hour))
		}
		issued, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), spec)
		if err == nil || issued.Raw != "" {
			t.Fatalf("forged Actions root: %+v %v", issued.Capability, err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "capabilities", 0)
}

func TestGitHubEnrollmentRemovalFencesRootsAndDescendants(t *testing.T) {
	tenant, a, row := actionsEnrollment(t)
	spec := actionsSpec(t, tenant.s, tenant.team, "workflow")
	root, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	child, err := tenant.s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{
		Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.NodesClaim}, Binding: spec.Binding, Repositories: spec.Repositories, ExpiresAt: spec.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	other := actionsSpec(t, tenant.s, tenant.team, "other")
	other.RegistrationID, other.Subject.ID = "github:43:7", "github:43:7:other:1"
	other.Binding.Push.WorkflowRef = "acme/other/.github/workflows/sparkwing.yaml@refs/heads/main"
	other.Binding.Push.RepositoryKey, other.Repositories = "gh:43", []string{"gh:43"}
	if _, err := tenant.RegisterGitHubRunner(t.Context(), a, GitHubRunnerBinding{RepositoryID: 43, RepositoryOwnerID: 7, Repository: "acme/other"}); err != nil {
		t.Fatal(err)
	}
	independent, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.RevokeGitHubRunner(t.Context(), a, row.RepositoryID); err != nil {
		t.Fatal(err)
	}
	for _, issued := range []capability.Issued{root, child} {
		_, err := tenant.s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated {
			t.Fatalf("removed enrollment authority: %v", err)
		}
	}
	if _, err := tenant.s.AsOperator().LookupCapability(t.Context(), independent.Capability.Digest); err != nil {
		t.Fatalf("unrelated enrollment revoked: %v", err)
	}
	if issued, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), spec); err == nil || issued.Raw != "" {
		t.Fatal("stale binding read minted after revocation")
	}
	if _, err := tenant.RegisterGitHubRunner(t.Context(), a, row); err != nil {
		t.Fatal(err)
	}
	_, err = tenant.s.AsOperator().LookupCapability(t.Context(), root.Capability.Digest)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated {
		t.Fatalf("re-enrolled root: %v", err)
	}
}

func TestGitHubEnrollmentRemovalRacesIssuance(t *testing.T) {
	for i := range 12 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, a, row := actionsEnrollment(t)
			start := make(chan struct{})
			var issued capability.Issued
			var issueErr, revokeErr error
			var group sync.WaitGroup
			group.Go(func() {
				<-start
				issued, issueErr = tenant.s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, tenant.s, tenant.team, "race"))
			})
			group.Go(func() { <-start; revokeErr = tenant.RevokeGitHubRunner(t.Context(), a, row.RepositoryID) })
			close(start)
			group.Wait()
			if revokeErr != nil {
				t.Fatal(revokeErr)
			}
			if issueErr == nil {
				_, err := tenant.s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest)
				var refusal *apierr.Error
				if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated {
					t.Fatalf("racing issuance after revocation: %v", err)
				}
			} else {
				var refusal *apierr.Error
				if issued.Raw != "" || !errors.As(issueErr, &refusal) || refusal.Code != apierr.Forbidden {
					t.Fatalf("issuance returned an unexpected result: %v", issueErr)
				}
			}
		})
	}
}

func TestGitHubEnrollmentAndIssuanceLimitsSerialize(t *testing.T) {
	for _, issuance := range []bool{false, true} {
		t.Run(fmt.Sprint(issuance), func(t *testing.T) {
			tenant, a, _ := actionsEnrollment(t)
			var group sync.WaitGroup
			results := make(chan error, 32)
			for i := range 32 {
				group.Go(func() {
					var err error
					if issuance {
						_, err = tenant.s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, tenant.s, tenant.team, fmt.Sprint(i)))
					} else {
						_, err = tenant.RegisterGitHubRunner(t.Context(), a, GitHubRunnerBinding{RepositoryID: int64(i + 100), RepositoryOwnerID: 7, Repository: "acme/repo"})
					}
					results <- err
				})
			}
			group.Wait()
			close(results)
			accepted := 0
			for err := range results {
				if err == nil {
					accepted++
				} else if !errors.Is(err, ErrBindingLimit) && !errors.Is(err, ErrGitHubRunnerCredentialLimit) {
					t.Fatal(err)
				}
			}
			want := maxGitHubRunnerBindings - 1
			if issuance {
				want = maxGitHubRunnerCredentials
			}
			if accepted != want {
				t.Fatalf("accepted %d, want %d", accepted, want)
			}
		})
	}
}

func TestGitHubEnrollmentLimitsIgnoreFundingAndExemptDefaultTeam(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000,1)`)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, DefaultTeam)
	operator := &Tenant{s: tenant.s, team: DefaultTeam}
	owner := authorityMember(t, tenant.s, "operator-owner", RoleOwner, DefaultTeam)
	op := a
	op.Principal.Authority, op.Target.Team = owner.Authority, string(DefaultTeam)
	for i := range maxGitHubRunnerBindings + 1 {
		row := GitHubRunnerBinding{RepositoryID: int64(i + 1), RepositoryOwnerID: 7, Repository: "acme/repo"}
		_, err := tenant.RegisterGitHubRunner(t.Context(), a, row)
		if i < maxGitHubRunnerBindings && err != nil || i == maxGitHubRunnerBindings && !errors.Is(err, ErrBindingLimit) {
			t.Fatalf("funded registration %d: %v", i, err)
		}
		if _, err := operator.RegisterGitHubRunner(t.Context(), op, row); err != nil {
			t.Fatalf("default-team registration %d: %v", i, err)
		}
	}
}

func TestGitHubActionsExpiredAndRevokedRootsReleaseIssuanceCapacity(t *testing.T) {
	tenant, a, row := actionsEnrollment(t)
	now := tenant.s.now()
	tenant.s.clock = func() time.Time { return now }
	for i := range maxGitHubRunnerCredentials {
		if _, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, tenant.s, tenant.team, fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Hour)
	if _, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, tenant.s, tenant.team, "expired-capacity")); err != nil {
		t.Fatalf("expired roots still consume issuance capacity: %v", err)
	}
	if err := tenant.RevokeGitHubRunner(t.Context(), a, row.RepositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RegisterGitHubRunner(t.Context(), a, row); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, tenant.s, tenant.team, "revoked-capacity")); err != nil {
		t.Fatalf("revoked roots still consume issuance capacity: %v", err)
	}
}

func TestGitHubEnrollmentRollbackPreservesConsentAndAuthority(t *testing.T) {
	for _, failure := range []string{"register authority", "revoke authority", "revoke root"} {
		t.Run(failure, func(t *testing.T) {
			tenant, a := credentialMaintenanceFixture(t)
			if failure == "register authority" {
				rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER demote_registration AFTER INSERT ON github_runner_bindings BEGIN UPDATE memberships SET role='reader' WHERE account_id='owner'; END`)
				row, err := tenant.RegisterGitHubRunner(t.Context(), a, GitHubRunnerBinding{RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/widgets"})
				if err == nil || row.RepositoryID != 0 {
					t.Fatal("authority loss returned registration")
				}
				rewriteSchemaCount(t, tenant.s.db, "github_runner_bindings", 0)
				return
			}
			tenant, a, row := actionsEnrollment(t)
			root, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, tenant.s, tenant.team, "rollback"))
			if err != nil {
				t.Fatal(err)
			}
			if failure == "revoke authority" {
				rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER demote_revocation AFTER DELETE ON github_runner_bindings BEGIN UPDATE memberships SET role='reader' WHERE account_id='owner'; END`)
			} else {
				rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_revocation BEFORE UPDATE ON capabilities BEGIN SELECT RAISE(ABORT,'refused'); END`)
			}
			if err := tenant.RevokeGitHubRunner(t.Context(), a, row.RepositoryID); err == nil {
				t.Fatal("failed revocation succeeded")
			}
			if _, err := tenant.GitHubRunnerBinding(t.Context(), row.RepositoryID); err != nil {
				t.Fatalf("failed revocation removed consent: %v", err)
			}
			if _, err := tenant.s.AsOperator().LookupCapability(t.Context(), root.Capability.Digest); err != nil {
				t.Fatalf("failed revocation ended root: %v", err)
			}
		})
	}
}

func TestGitHubActionsExpiryFreesQuotaWithoutDeletingAudit(t *testing.T) {
	tenant, _, _ := actionsEnrollment(t)
	s := tenant.s
	now := s.now()
	s.clock = func() time.Time { return now }
	var first capability.Issued
	for i := range maxGitHubRunnerCredentials {
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, s, tenant.team, fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = issued
		}
	}
	if issued, err := s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, s, tenant.team, "full")); !errors.Is(err, ErrGitHubRunnerCredentialLimit) || issued.Raw != "" {
		t.Fatalf("full quota: %+v, %v", issued.Capability, err)
	}
	now = *first.Capability.ExpiresAt
	if _, err := s.AsOperator().LookupCapability(t.Context(), first.Capability.Digest); err == nil {
		t.Fatal("expired Actions authority authenticated")
	}
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), actionsSpec(t, s, tenant.team, "after-expiry"))
	if err != nil || issued.Raw == "" {
		t.Fatalf("quota after expiry: %+v, %v", issued.Capability, err)
	}
	rewriteSchemaCount(t, s.db, "capabilities", maxGitHubRunnerCredentials+1)
}
