package nodeproto

import "fmt"

// Pipeline selects SDK metadata by its configured entrypoint.
func (d DescribeDoc) Pipeline(entrypoint string) (PipelineDescription, bool) {
	for _, pipeline := range d.Pipelines {
		if pipeline.Name == entrypoint {
			return pipeline, true
		}
	}
	return PipelineDescription{}, false
}

// ValidateSecretDeclarations checks canonical names after SDK-specific projection.
func ValidateSecretDeclarations(declarations []SecretDeclaration) error {
	names := map[string]bool{}
	for _, declaration := range declarations {
		if declaration.Name == "" || names[declaration.Name] {
			return fmt.Errorf("SDK describe has invalid secret declarations")
		}
		names[declaration.Name] = true
	}
	return nil
}
