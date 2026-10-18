package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type planSecretCipher struct {
	cipher Cipher
}

func (c planSecretCipher) OpenPlanSecret(ctx context.Context, team store.Team, secret store.Secret) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := OpenSecretValue(c.cipher, team, &secret)
	if err == nil && secret.Masked {
		requestMasker(ctx).Register(value)
	}
	return value, err
}
