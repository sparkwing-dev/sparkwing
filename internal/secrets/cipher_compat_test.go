package secrets_test

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

var _ controller.Cipher = (*secrets.Cipher)(nil)

var _ controller.BoundCipher = (*secrets.Cipher)(nil)
