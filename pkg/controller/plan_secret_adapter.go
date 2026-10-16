package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type planSecretCipher struct {
	cipher Cipher
	masker *secrets.Masker
}

func (c planSecretCipher) OpenPlanSecret(ctx context.Context, team store.Team, secret store.Secret) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := OpenSecretValue(c.cipher, team, &secret)
	if err == nil && secret.Masked && c.masker != nil {
		c.masker.Register(value)
	}
	return value, err
}
