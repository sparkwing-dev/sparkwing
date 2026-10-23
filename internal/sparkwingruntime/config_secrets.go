package sparkwingruntime

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// MissingSecretsError names every required secret of a pipeline's Secrets()
// declaration that its secret source does not hold.
type MissingSecretsError struct {
	Pipeline string
	Names    []string
}

func (e *MissingSecretsError) Error() string {
	noun := "secrets"
	if len(e.Names) == 1 {
		noun = "secret"
	}
	return fmt.Sprintf("pipeline %q secrets: no value for required %s %s", e.Pipeline, noun, strings.Join(e.Names, ", "))
}

// Unwrap lets a caller match the miss as [secrets.ErrSecretMissing].
func (e *MissingSecretsError) Unwrap() error { return secrets.ErrSecretMissing }
