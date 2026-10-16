package nodehost

import "github.com/sparkwing-dev/sparkwing/pkg/projectconfig"

// PipelineIdentity separates the registered pipeline name from its Go type identity.
type PipelineIdentity struct {
	Invoked    string
	Entrypoint string
}

// ResolvePipeline selects the configured entrypoint while preserving the invoked name.
func ResolvePipeline(config *projectconfig.Config, invoked string) PipelineIdentity {
	selection := PipelineIdentity{Invoked: invoked, Entrypoint: invoked}
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
