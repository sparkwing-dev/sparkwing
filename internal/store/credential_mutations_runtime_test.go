package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCredentialMutationsUseCurrentActorAndStoreClock(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	now := time.Unix(1700000000, 123456789).UTC()
	tenant.s.clock = func() time.Time { return now }
	sec := Secret{Name: "KEY", Value: "sealed", Pipeline: "build", Masked: true}
	if err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, sec)); err != nil {
		t.Fatal(err)
	}
	first, err := tenant.GetSecretRow(sec.Name, sec.Pipeline)
	if err != nil || first.Principal != "owner" || !first.CreatedAt.Equal(now) || !first.UpdatedAt.Equal(now) {
		t.Fatalf("secret: %+v %v", first, err)
	}
	now = now.Add(time.Second)
	sec.Value = "replacement"
	replacement := a
	replacement.Principal.Authority = authorityMember(t, tenant.s, "replacement-author", RoleOwner).Authority
	if err := tenant.WriteSecret(t.Context(), replacement, sealedSecretFixture(t, tenant, sec)); err != nil {
		t.Fatal(err)
	}
	second, err := tenant.GetSecretRow(sec.Name, sec.Pipeline)
	if err != nil || openSecretFixture(t, tenant, second) != "replacement" || second.Principal != "replacement-author" || !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.Equal(now) {
		t.Fatalf("replacement: %+v %v", second, err)
	}
	c, err := tenant.WriteGitCredential(t.Context(), a, sealedGitFixture(t, tenant, GitCredential{HostWide: true, Host: "git.test", Kind: GitCredentialSSH, Secret: "sealed", Fingerprint: "SHA256:key"}))
	if err != nil || c.ID == "" || c.Team != tenant.team || c.CreatedBy != "owner" || c.ConfirmedAt != nil || !c.CreatedAt.Equal(now) {
		t.Fatalf("git: %+v %v", c, err)
	}
	if row, err := tenant.ConfirmGitCredentialFor(t.Context(), a, c.Host, "", "wrong"); !errors.Is(err, ErrFingerprintMismatch) || row.ID != "" {
		t.Fatalf("mismatched confirmation: %+v %v", row, err)
	}
	c, err = tenant.ConfirmGitCredentialFor(t.Context(), a, c.Host, "", c.Fingerprint)
	if err != nil || c.ConfirmedAt == nil || !c.ConfirmedAt.Equal(now) {
		t.Fatalf("confirmation: %+v %v", c, err)
	}
	now = now.Add(time.Second)
	c, err = tenant.WriteGitCredential(t.Context(), a, sealedGitFixture(t, tenant, c))
	if err != nil || c.ConfirmedAt == nil || !c.ConfirmedAt.Equal(now.Add(-time.Second)) {
		t.Fatalf("same-key replacement: %+v %v", c, err)
	}
	c.Fingerprint = "SHA256:new"
	c, err = tenant.WriteGitCredential(t.Context(), a, sealedGitFixture(t, tenant, c))
	if err != nil || c.ConfirmedAt != nil {
		t.Fatalf("changed-key replacement: %+v %v", c, err)
	}
	if err := tenant.RemoveSecret(t.Context(), a, sec.Name, sec.Pipeline); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RemoveGitCredential(t.Context(), a, c.Host, ""); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RemoveSecret(t.Context(), a, sec.Name, sec.Pipeline); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing secret: %v", err)
	}
	if err := tenant.RemoveGitCredential(t.Context(), a, c.Host, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing git: %v", err)
	}
}

func TestCredentialMutationsAuthorityProperty(t *testing.T) {
	tenant, base := credentialMaintenanceFixture(t)
	random := rand.New(rand.NewPCG(143, 991))
	for i := range 96 {
		a := base
		a.Access.Grants = append([]capability.Grant(nil), base.Access.Grants...)
		target := *tenant
		rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='owner' WHERE account_id='owner'`)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE sessions SET revoked_at=NULL WHERE hash='owner'`)
		switch random.IntN(7) {
		case 0:
			a.Target.Team = "b"
		case 1:
			target.team = "b"
		case 2:
			a.Access.Grants = []capability.Grant{capability.SecretsRead}
		case 3:
			a.Access.Credential = capability.CredentialClaim
		case 4:
			a.Access.Liveness = capability.LiveSensitive
		case 5:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='reader' WHERE account_id='owner'`)
		case 6:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE sessions SET revoked_at=1 WHERE hash='owner'`)
		}
		name := fmt.Sprintf("KEY_%d", i)
		if err := target.WriteSecret(t.Context(), a, sealedSecretFixture(t, &target, Secret{Name: name, Value: "sealed"})); err == nil {
			t.Fatalf("write %d passed invalid authority", i)
		}
		row, err := target.WriteGitCredential(t.Context(), a, sealedGitFixture(t, &target, GitCredential{HostWide: true, Host: "git.test", Kind: GitCredentialHTTPS, Secret: "sealed"}))
		if err == nil || row.ID != "" {
			t.Fatalf("git write %d returned a row: %+v %v", i, row, err)
		}
		for _, err := range []error{target.RemoveSecret(t.Context(), a, name, ""), target.RemoveGitCredential(t.Context(), a, "git.test", "")} {
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("delete %d did not refuse authority first: %v", i, err)
			}
		}
		if row, err := target.ConfirmGitCredentialFor(t.Context(), a, "git.test", "", "key"); err == nil || errors.Is(err, ErrNotFound) || row.ID != "" {
			t.Fatalf("confirmation %d did not refuse authority: %+v %v", i, row, err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "secrets", 0)
	rewriteSchemaCount(t, tenant.s.db, "git_credentials", 0)
}

func TestCredentialMutationsFailedReplacementPreservesPreviousRow(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	before, err := tenant.WriteGitCredential(t.Context(), a, sealedGitFixture(t, tenant, GitCredential{HostWide: true, Host: "git.test", Kind: GitCredentialHTTPS, Secret: "old"}))
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_credential BEFORE INSERT ON git_credentials BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if row, err := tenant.WriteGitCredential(t.Context(), a, sealedGitFixture(t, tenant, GitCredential{HostWide: true, Host: "git.test", Kind: GitCredentialHTTPS, Secret: "new"})); err == nil || row.ID != "" {
		t.Fatalf("failed replacement: %+v %v", row, err)
	}
	after, err := tenant.GitCredentialForRepository(t.Context(), "", before.Host)
	if err != nil || after.ID != before.ID || after.Secret != before.Secret {
		t.Fatalf("replacement was partially committed: %+v %v", after, err)
	}
}

func TestCredentialMutationsRecheckAuthorityAfterWriting(t *testing.T) {
	for _, resource := range []string{"secrets", "git_credentials"} {
		t.Run(resource, func(t *testing.T) {
			tenant, a := credentialMaintenanceFixture(t)
			rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER end_admin AFTER INSERT ON `+resource+` BEGIN UPDATE memberships SET role='reader' WHERE account_id='owner'; END`)
			var err error
			if resource == "secrets" {
				err = tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: "KEY", Value: "sealed"}))
			} else {
				var row GitCredential
				row, err = tenant.WriteGitCredential(t.Context(), a, sealedGitFixture(t, tenant, GitCredential{HostWide: true, Host: "git.test", Kind: GitCredentialHTTPS, Secret: "sealed"}))
				if row.ID != "" {
					t.Fatal("failed commit returned credential")
				}
			}
			if err == nil {
				t.Fatal("authority ended before commit")
			}
			rewriteSchemaCount(t, tenant.s.db, resource, 0)
		})
	}
}

func TestCredentialMutationsSerializeExistingSecretLimit(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	for i := range MaxSecretsPerTeam - 1 {
		if err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: fmt.Sprintf("KEY_%d", i), Value: "sealed"})); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			results <- tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: fmt.Sprintf("CONCURRENT_%d", i), Value: "sealed"}))
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrSecretLimit) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d secrets at one remaining slot", accepted)
	}
	if err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: "KEY_0", Value: "replacement"})); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMutationsSecretLimitsPreserveExistingValues(t *testing.T) {
	for _, team := range []Team{"a", DefaultTeam} {
		t.Run(string(team), func(t *testing.T) {
			tenant, a := credentialMaintenanceFixture(t)
			if team == DefaultTeam {
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, team)
				owner := authorityMember(t, tenant.s, "default-owner", RoleOwner, team)
				a.Principal.Authority = owner.Authority
				tenant.team, a.Target.Team = team, string(team)
			}
			for i := range MaxSecretsPerTeam {
				if err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: fmt.Sprintf("KEY_%d", i), Value: "original"})); err != nil {
					t.Fatal(err)
				}
			}
			for _, row := range []Secret{{Name: "EXTRA", Value: "extra"}, {Name: "KEY_0", Value: strings.Repeat("x", MaxSecretValueBytes+1)}} {
				err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, row))
				if team == DefaultTeam && err != nil || team != DefaultTeam && !errors.Is(err, ErrSecretLimit) {
					t.Fatalf("secret %s: %v", row.Name, err)
				}
			}
			row, err := tenant.GetSecretRow("KEY_0", "")
			if err != nil || team != DefaultTeam && openSecretFixture(t, tenant, row) != "original" || team == DefaultTeam && len(openSecretFixture(t, tenant, row)) != MaxSecretValueBytes+1 {
				t.Fatalf("size refusal: %+v, %v", row, err)
			}
			if err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: "KEY_0", Value: "replacement"})); err != nil {
				t.Fatal(err)
			}
			rows, err := tenant.ListSecrets()
			want := MaxSecretsPerTeam
			if team == DefaultTeam {
				want++
			}
			if err != nil || len(rows) != want {
				t.Fatalf("secret count: %d, %v", len(rows), err)
			}
			for i := 1; i < len(rows); i++ {
				if rows[i-1].Name >= rows[i].Name {
					t.Fatal("secret list is not ordered by name")
				}
			}
		})
	}
}

func TestCredentialMutationsDeletionPreservesOtherScopesProperty(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	foreignActor := authorityMember(t, tenant.s, "foreign", RoleOwner, "b")
	foreign := &Tenant{s: tenant.s, team: "b"}
	b := a
	b.Principal.Authority, b.Target.Team = foreignActor.Authority, "b"
	random := rand.New(rand.NewPCG(107, 315))
	for range 32 {
		name := fmt.Sprintf("KEY_%x", random.Uint64())
		pipeline := fmt.Sprintf("pipeline/%x'", random.Uint64())
		for _, scope := range []struct {
			tenant *Tenant
			a      capability.Authorized
		}{{tenant, a}, {foreign, b}} {
			for _, p := range []string{"", pipeline} {
				if err := scope.tenant.WriteSecret(t.Context(), scope.a, sealedSecretFixture(t, scope.tenant, Secret{Name: name, Pipeline: p, Value: string(scope.tenant.team) + p})); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tenant.RemoveSecret(t.Context(), a, name, pipeline); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.GetSecretRow(name, pipeline); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted secret: %v", err)
		}
		if err := tenant.RemoveSecret(t.Context(), a, name, pipeline); !errors.Is(err, ErrNotFound) {
			t.Fatalf("repeated deletion: %v", err)
		}
		for _, want := range []struct {
			tenant   *Tenant
			pipeline string
		}{{tenant, ""}, {foreign, ""}, {foreign, pipeline}} {
			row, err := want.tenant.GetSecretRow(name, want.pipeline)
			if err != nil || openSecretFixture(t, want.tenant, row) != string(want.tenant.team)+want.pipeline {
				t.Fatalf("another scope changed: %+v, %v", row, err)
			}
		}
	}
}
