package main

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
