package orchestrator

import (
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// storeurlProfileLookup adapts the orchestrator's profile-lookup
// callback (which the SDK also consumes for type=profile secret
// sources) to the storeurl factory's named type. Returns nil when the
// caller didn't install one; the factory then errors loudly the
// moment a controller-typed spec arrives.
func storeurlProfileLookup(lookup sparkwing.ProfileLookup) storeurl.ProfileLookup {
	if lookup == nil {
		return nil
	}
	return func(name string) (string, string, error) {
		return lookup(name)
	}
}

// decodeTargetBackend converts the target's per-surface
// map[string]any blobs into typed backends.Surfaces by yaml
// round-trip. An empty / missing entry produces nil for that
// surface so Effective leaves the lower layer in place.
func decodeTargetBackend(p *pipelines.Pipeline, targetName string) backends.Surfaces {
	if p == nil || targetName == "" {
		return backends.Surfaces{}
	}
	t, ok := p.Targets[targetName]
	if !ok || t.Backend == nil {
		return backends.Surfaces{}
	}
	return backends.Surfaces{
		Cache: decodeSpec(t.Backend.Cache),
		Logs:  decodeSpec(t.Backend.Logs),
		State: decodeSpec(t.Backend.State),
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
