package teststore

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SealGitCredential seals a fixture row with a fixed test key.
func SealGitCredential(t T, team store.Team, row store.GitCredential) store.SealedGitCredential {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := store.SealGitCredential(cipher, team, row)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
