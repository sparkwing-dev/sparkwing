package nodehost

import "github.com/sparkwing-dev/sparkwing/pkg/projectconfig"

// pipelineIdentity separates the registered pipeline name from its Go type identity.
type pipelineIdentity struct {
	Invoked    string
	Entrypoint string
}

// resolvePipeline selects the configured entrypoint while preserving the invoked name.
func resolvePipeline(config *projectconfig.Config, invoked string) pipelineIdentity {
	selection := pipelineIdentity{Invoked: invoked, Entrypoint: invoked}
	if config != nil {
		for _, pipeline := range config.Pipelines {
			if pipeline.Name == invoked {
				selection.Entrypoint = pipeline.Entrypoint
				break
			}
		}
	}
	return selection
}
