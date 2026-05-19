package secrets_test

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// Compile-time assertion: *secrets.Cipher satisfies the
// pkg/controller.Cipher interface. Pins the contract so that adding
// a method to the interface or removing one from the concrete type
// fails to compile loudly here instead of breaking the controller
// pod or pkg/localws at runtime.
var _ controller.Cipher = (*secrets.Cipher)(nil)
