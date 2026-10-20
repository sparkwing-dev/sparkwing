package secrets_test

import (
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

var _ controller.Cipher = (*secrets.Cipher)(nil)

var _ controller.BoundCipher = (*secrets.Cipher)(nil)
