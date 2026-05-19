package orchestrator

import (
	"context"
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ApplyBackendsConfig resolves backends.yaml + built-in detect
// rules + the legacy env-var shim, picks an environment via
// auto-detect, layers the per-target Backend overlay on top, and
// populates opts.LogStore + opts.ArtifactStore when not already set
// by the caller.
//
// Resolution precedence (per-surface, first non-zero wins):
//
//	target overlay > environment auto-detect > defaults > legacy env-var shim
//
// Existing opts.LogStore / opts.ArtifactStore values pre-set by the
// caller (e.g. cluster worker plumbing) take precedence over the
// resolved configuration.
func ApplyBackendsConfig(ctx context.Context, opts *Options) error {
	file, err := backends.ResolveWithOverlay(opts.SparkwingDir, opts.BackendsConfig)
	if err != nil {
		return fmt.Errorf("backends.yaml: %w", err)
	}

	target := decodeTargetBackend(opts.PipelineYAML, opts.Target)

	envName, _, _ := backends.DetectEnvironment(file)
	eff := backends.Effective(file, envName, target)

	lookup := storeurlProfileLookup(opts.ProfileLookup)
	if opts.ArtifactStore == nil && eff.Cache != nil {
		store, err := storeurl.OpenArtifactStoreFromSpec(ctx, *eff.Cache, lookup)
		if err != nil {
			return fmt.Errorf("cache backend: %w", err)
		}
		opts.ArtifactStore = store
	}
	if opts.LogStore == nil && eff.Logs != nil {
		store, err := storeurl.OpenLogStoreFromSpec(ctx, *eff.Logs, lookup)
		if err != nil {
			return fmt.Errorf("logs backend: %w", err)
		}
		opts.LogStore = store
	}
	if opts.State == nil {
		spec := eff.State
		if spec == nil && opts.DefaultStateDB != "" {
			spec = &backends.Spec{Type: backends.TypeSQLite, Path: opts.DefaultStateDB}
		} else if spec != nil && spec.Type == backends.TypeSQLite && spec.Path == "" && opts.DefaultStateDB != "" {
			// User declared sqlite without an explicit path; fall back to the
			// process default so `state: { type: sqlite }` round-trips to the
			// historical ~/.sparkwing/state.db location.
			filled := *spec
			filled.Path = opts.DefaultStateDB
			spec = &filled
		}
		if spec != nil {
			st, err := storeurl.OpenStateStoreFromSpec(ctx, *spec)
			if err != nil {
				return fmt.Errorf("state backend: %w", err)
			}
			opts.State = st
		}
	}
	return nil
}

// storeurlProfileLookup adapts the orchestrator's profile-lookup
// callback (which the SDK also consumes for remote-controller secret
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
