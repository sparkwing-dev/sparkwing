package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func fixtureSecretCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func sealedSecretFixture(t *testing.T, tenant *Tenant, row Secret) SealedSecret {
	t.Helper()
	sealed, err := SealSecret(fixtureSecretCipher(t), tenant.team, row)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func sealedGitFixture(t *testing.T, tenant *Tenant, row GitCredential) SealedGitCredential {
	t.Helper()
	sealed, err := SealGitCredential(fixtureSecretCipher(t), tenant.team, row)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func openSecretFixture(t *testing.T, tenant *Tenant, row *Secret) string {
	t.Helper()
	if row == nil {
		t.Fatal("missing secret")
	}
	plain, err := fixtureSecretCipher(t).OpenBound(secrets.PurposeSecret, string(tenant.team), row.Name, row.SealScope(), row.Shared, row.Masked, row.Value)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}
