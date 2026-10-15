package orchestrator

import (
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// PipelineRequires reads the checkout's declared placement labels.
func PipelineRequires(dir, pipeline string) ([]string, error) {
	path, cfg, err := projectconfig.Discover(dir)
	if err != nil {
		return nil, fmt.Errorf("read the pipeline's placement: %w", err)
	}
	if path == "" || cfg == nil {
		return nil, nil
	}
	if p := cfg.Registry().Find(pipeline); p != nil {
		return slices.Clone(p.Requires), nil
	}
	if cfg.Defaults != nil {
		return slices.Clone(cfg.Defaults.Requires), nil
	}
	return nil, nil
}
