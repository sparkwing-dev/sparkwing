package secrets

import "errors"

// ErrSecretMissing identifies an absent stored secret.
var ErrSecretMissing = errors.New("secret missing")
