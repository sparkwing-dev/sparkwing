package secrets

import "errors"

// ErrSecretMissing classifies a missing stored value separately from transport and authorization failures.
var ErrSecretMissing = errors.New("secret missing")
