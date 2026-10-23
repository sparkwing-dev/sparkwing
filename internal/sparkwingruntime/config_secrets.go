package sparkwingruntime

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// missingSecretsError names every required secret of a pipeline's Secrets()
// declaration that its secret source does not hold.
type missingSecretsError struct {
	Pipeline string
	Names    []string
}

func (e *missingSecretsError) Error() string {
	noun := "secrets"
	if len(e.Names) == 1 {
		noun = "secret"
	}
	return fmt.Sprintf("pipeline %q secrets: no value for required %s %s", e.Pipeline, noun, strings.Join(e.Names, ", "))
}

// Unwrap lets a caller match the miss as [sparkwing.ErrSecretMissing].
func (e *missingSecretsError) Unwrap() error { return sparkwing.ErrSecretMissing }
