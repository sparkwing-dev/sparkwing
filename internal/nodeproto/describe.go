package nodeproto

import (
	"fmt"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// ValidateSecretDeclarations checks canonical names after SDK-specific projection.
func ValidateSecretDeclarations(declarations []nodewire.SecretDeclaration) error {
	names := map[string]bool{}
	for _, declaration := range declarations {
		if declaration.Name == "" || names[declaration.Name] {
			return fmt.Errorf("SDK describe has invalid secret declarations")
		}
		names[declaration.Name] = true
	}
	return nil
}
