package orchestrator

import (
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// decodeDispatchBackend converts the pipeline's per-surface
// map[string]any blobs into typed backends.Surfaces by yaml
// round-trip. An empty / missing entry produces nil for that
// surface so Effective leaves the lower layer in place. The
// target-name argument is retained for caller compatibility but
// ignored -- v0.6 dispatch metadata lives on the pipeline, not on
// per-target sub-blocks.
func decodeTargetBackend(p *pipelines.Pipeline, targetName string) backends.Surfaces {
	_ = targetName
	if p == nil || p.Dispatch == nil || p.Dispatch.Backend == nil {
		return backends.Surfaces{}
	}
	b := p.Dispatch.Backend
	return backends.Surfaces{
		Cache: decodeSpec(b.Cache),
		Logs:  decodeSpec(b.Logs),
		State: decodeSpec(b.State),
	}
}

func decodeSpec(raw map[string]any) *backends.Spec {
	if len(raw) == 0 {
		return nil
	}
	out, err := yaml.Marshal(raw)
	if err != nil {
		return nil
	}
	var spec backends.Spec
	if err := yaml.Unmarshal(out, &spec); err != nil {
		return nil
	}
	if spec.Type == "" {
		return nil
	}
	return &spec
}
