package clisurface

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// PipelineIdentity separates the registered pipeline name from its Go type identity.
type PipelineIdentity struct {
	Name       string
	Entrypoint string
}

// ResolvePipeline captures the configured pair without consulting process state.
func ResolvePipeline(cfg *projectconfig.Config, name string) (PipelineIdentity, error) {
	identity := PipelineIdentity{Name: name, Entrypoint: name}
	if name == "" {
		return identity, errors.New("pipeline name is required")
	}
	if cfg == nil {
		return identity, nil
	}
	if err := projectconfig.Validate(*cfg); err != nil {
		return PipelineIdentity{}, err
	}
	for _, pipeline := range cfg.Pipelines {
		if pipeline.Name == name {
			identity.Entrypoint = pipeline.Entrypoint
			break
		}
	}
	return identity, nil
}
