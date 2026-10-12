package main

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var errKeyless = fmt.Errorf("this controller keeps its state in PostgreSQL and has no %s credential, "+
	"so it stores and opens no secret: put a base64-encoded 32-byte key in the %s file of its "+
	"--credentials-dir (generate one with `openssl rand -base64 32`) and restart it%.0w",
	credSecretsKey, credSecretsKey, secrets.ErrKeyRefused)

// safety: a PostgreSQL controller without a key serves everything else, so
// each secret write is refused with the reason rather than stored.
type keylessCipher struct{}

func (keylessCipher) Seal(string) (string, error) { return "", errKeyless }
func (keylessCipher) Open(string) (string, error) { return "", errKeyless }
func (keylessCipher) SealBound(string, string, string, bool, bool, string) (string, error) {
	return "", errKeyless
}

func (keylessCipher) OpenBound(string, string, string, bool, bool, string) (string, error) {
	return "", errKeyless
}

// safety: a plaintext row can only be sealed with a key, and a keyless
// controller would otherwise go on refusing to serve it without a word.
func refusePlaintextSecrets(ctx context.Context, st *store.Store) error {
	plaintext := 0
	_, err := st.ResealSecrets(ctx, secrets.BoundPrefix, 200, func(_ store.Team, sec store.Secret) (string, error) {
		if !secrets.IsEncrypted(sec.Value) {
			plaintext++
		}
		return "", errKeyless
	})
	if err != nil {
		return fmt.Errorf("check stored secrets: %w", err)
	}
	if plaintext == 0 {
		return nil
	}
	return fmt.Errorf("the store holds %d secrets as plaintext, written by an older controller without a key; "+
		"put the %s credential in --credentials-dir so the controller seals them at startup, then start again",
		plaintext, credSecretsKey)
}
