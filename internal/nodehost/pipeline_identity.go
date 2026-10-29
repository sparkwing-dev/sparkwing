package nodehost

import "github.com/sparkwing-dev/sparkwing/pkg/projectconfig"

// safety: Entrypoint is the SDK registration key, which may differ from the invoked name.
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
