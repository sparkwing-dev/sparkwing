package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func credentialMaintenanceFixture(t *testing.T) (*Tenant, capability.Authorized) {
	t.Helper()
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	return &Tenant{s: s, team: "a"}, capability.Authorized{Principal: capability.Principal{Authority: owner.Authority}, Target: capability.Target{Team: "a"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
}

func TestCredentialRotationPreservesTenantAndRowMetadataProperty(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	now := time.Unix(1700000000, 123456789).UTC()
	tenant.s.clock = func() time.Time { return now }
	random := rand.New(rand.NewPCG(116, 981))
	originals := map[Team][]Secret{}
	for _, team := range []Team{"a", "b"} {
		tn := &Tenant{s: tenant.s, team: team}
		for i := range 24 {
			sec := Secret{Name: fmt.Sprintf("secret_%d", i), Pipeline: []string{"", "deploy"}[random.IntN(2)], Value: fmt.Sprintf("old/%x", random.Uint64()), Principal: "author", Masked: random.IntN(2) == 0, Shared: random.IntN(2) == 0}
			if err := tn.CreateOrReplaceSecret(sec, now); err != nil {
				t.Fatal(err)
			}
			row, err := tn.GetSecretRow(sec.Name, sec.Pipeline)
			if err != nil {
				t.Fatal(err)
			}
			if !row.CreatedAt.Equal(now) {
				t.Fatalf("nanosecond secret timestamp: %+v", row)
			}
			originals[team] = append(originals[team], *row)
		}
		if _, err := tn.PutGitCredential(t.Context(), GitCredential{Host: "git.example.test", Kind: GitCredentialHTTPS, Secret: "old/key", CreatedBy: "author"}, now); err != nil {
			t.Fatal(err)
		}
	}
	before, err := tenant.GitCredentialForHost(t.Context(), "git.example.test")
	if err != nil {
		t.Fatal(err)
	}
	secretCalls, gitCalls := 0, 0
	n, m, err := tenant.RotateCredentials(t.Context(), a, func(row Secret) (string, error) { secretCalls++; return "new/" + row.Value, nil }, func(row GitCredential) (string, error) { gitCalls++; return "new/" + row.Secret, nil })
	if err != nil || n != 24 || m != 1 || secretCalls != 24 || gitCalls != 1 {
		t.Fatalf("rotation counts: %d %d %d %d, %v", n, m, secretCalls, gitCalls, err)
	}
	for team, rows := range originals {
		tn := &Tenant{s: tenant.s, team: team}
		for _, want := range rows {
			if team == "a" {
				want.Value = "new/" + want.Value
			}
			got, err := tn.GetSecretRow(want.Name, want.Pipeline)
			if err != nil || !reflect.DeepEqual(*got, want) {
				t.Fatalf("secret row changed: got=%+v want=%+v, %v", got, want, err)
			}
		}
		got, err := tn.GitCredentialForHost(t.Context(), "git.example.test")
		if err != nil {
			t.Fatal(err)
		}
		if team == "a" {
			before.Secret = "new/" + before.Secret
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("git metadata changed: %+v", got)
			}
		} else if got.Secret != "old/key" {
			t.Fatal("foreign git secret changed")
		}
	}
}

func TestCredentialRotationRollsBackBothTables(t *testing.T) {
	for _, failure := range []string{"secret", "git", "expiry", "demoted", "foreign", "wrong_rule"} {
		t.Run(failure, func(t *testing.T) {
			tenant, a := credentialMaintenanceFixture(t)
			now := time.Unix(1700000000, 123456789).UTC()
			tenant.s.clock = func() time.Time { return now }
			for _, name := range []string{"first", "second"} {
				if err := tenant.CreateOrReplaceSecret(Secret{Name: name, Value: "old"}, now); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tenant.PutGitCredential(t.Context(), GitCredential{Host: "git.example.test", Kind: GitCredentialHTTPS, Secret: "old"}, now); err != nil {
				t.Fatal(err)
			}
			if failure == "demoted" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='reader' WHERE account_id='owner'`)
			}
			if failure == "foreign" {
				a.Target.Team = "b"
			}
			if failure == "wrong_rule" {
				a.Access.Grants = []capability.Grant{capability.SecretsRead}
			}
			boom := errors.New("cannot transform")
			n, m, err := tenant.RotateCredentials(t.Context(), a, func(row Secret) (string, error) {
				if failure == "secret" && row.Name == "second" {
					return "", boom
				}
				return "new", nil
			}, func(GitCredential) (string, error) {
				if failure == "git" {
					return "", boom
				}
				if failure == "expiry" {
					now = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				return "new", nil
			})
			if err == nil || n != 0 || m != 0 {
				t.Fatalf("failed rotation: %d,%d,%v", n, m, err)
			}
			for _, name := range []string{"first", "second"} {
				got, err := tenant.GetSecret(name)
				if err != nil || got.Value != "old" {
					t.Fatalf("secret partially rotated: %+v,%v", got, err)
				}
			}
			got, err := tenant.GitCredentialForHost(t.Context(), "git.example.test")
			if err != nil || got.Secret != "old" {
				t.Fatalf("git partially rotated: %+v,%v", got, err)
			}
		})
	}
}

func TestCredentialRotationEmptyAndMissingTransforms(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	secret := func(Secret) (string, error) { t.Fatal("empty secret transform"); return "", nil }
	git := func(GitCredential) (string, error) { t.Fatal("empty git transform"); return "", nil }
	if n, m, err := tenant.RotateCredentials(t.Context(), a, secret, git); err != nil || n != 0 || m != 0 {
		t.Fatalf("empty rotation: %d,%d,%v", n, m, err)
	}
	if _, _, err := tenant.RotateCredentials(t.Context(), a, nil, git); err == nil {
		t.Fatal("missing transform accepted")
	}
}

func TestSourceReleaseAuditsReadCommittedNanosecondsAndTenant(t *testing.T) {
	tenant, a, _ := sourceReleaseFixture(t)
	tenant.s.sourcePolicy = nil
	_, first, err := tenant.ReleaseSourceCredential(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	secondTime := tenant.s.now().Add(time.Nanosecond)
	tenant.s.clock = func() time.Time { return secondTime }
	_, second, err := tenant.ReleaseSourceCredential(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tenant.SourceReleaseAudits(t.Context(), 1)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], second) {
		t.Fatalf("newest audit: %+v, %v", rows, err)
	}
	rows, err = tenant.SourceReleaseAudits(t.Context(), 0)
	if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows[1], first) {
		t.Fatalf("committed audits: %+v,%v", rows, err)
	}
	rows, err = (&Tenant{s: tenant.s, team: "b"}).SourceReleaseAudits(t.Context(), 200)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("foreign audits: %+v,%v", rows, err)
	}
}
