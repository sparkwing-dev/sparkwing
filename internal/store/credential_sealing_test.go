package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestCredentialWritersRejectUnsealedValuesProperty(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	foreign := &Tenant{s: tenant.s, team: "b"}
	cipher := fixtureSecretCipher(t)
	random := rand.New(rand.NewPCG(739, 201))
	for i := range 32 {
		plain := fmt.Sprintf("plain/%x", random.Uint64())
		if err := tenant.WriteSecret(t.Context(), a, SealedSecret{team: tenant.team, row: Secret{Name: "KEY", Value: plain}}); err == nil {
			t.Fatal("plaintext secret stored")
		}
		if _, err := tenant.WriteGitCredential(t.Context(), a, SealedGitCredential{team: tenant.team, row: GitCredential{HostWide: true, Host: "git.test", Kind: GitCredentialHTTPS, Secret: plain}}); err == nil {
			t.Fatal("plaintext git credential stored")
		}
		secret, err := SealSecret(cipher, tenant.team, Secret{Name: fmt.Sprintf("KEY_%d", i), Value: plain, Masked: i%2 == 0, Shared: i%3 == 0})
		if err != nil {
			t.Fatal(err)
		}
		if err := foreign.WriteSecret(t.Context(), a, secret); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("foreign sealed secret: %v", err)
		}
		copy := secret.Row()
		copy.Value = plain
		copy.Masked = !copy.Masked
		if err := tenant.WriteSecret(t.Context(), a, secret); err != nil {
			t.Fatal(err)
		}
		stored, err := tenant.GetSecretRow(secret.Row().Name, "")
		if err != nil || stored.Value == plain || openSecretFixture(t, tenant, stored) != plain {
			t.Fatalf("sealed round trip: %v", err)
		}
		git, err := SealGitCredential(cipher, tenant.team, GitCredential{HostWide: true, Host: fmt.Sprintf("git-%d.test", i), Kind: GitCredentialHTTPS, Secret: plain})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := foreign.WriteGitCredential(t.Context(), a, git); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("foreign sealed credential: %v", err)
		}
		storedGit, err := tenant.WriteGitCredential(t.Context(), a, git)
		if err != nil || storedGit.Secret == plain {
			t.Fatalf("sealed source credential: %v", err)
		}
		opened, err := cipher.OpenBound(secrets.PurposeGitCredential, string(tenant.team), "git-credential:"+storedGit.Host, "git-credential", false, true, storedGit.Secret)
		if err != nil || opened != plain {
			t.Fatalf("source round trip: %v", err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "secrets", 32)
	rewriteSchemaCount(t, tenant.s.db, "git_credentials", 32)
}
