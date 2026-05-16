package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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
	file, err := backends.ResolveWithEnvAndOverlay(opts.SparkwingDir, opts.BackendsConfig)
	if err != nil {
		return fmt.Errorf("backends.yaml: %w", err)
	}

	target := decodeTargetBackend(opts.PipelineYAML, opts.Target)

	var envName string
	if opts.BackendsEnv != "" {
		if _, ok := file.Environments[opts.BackendsEnv]; !ok {
			names := make([]string, 0, len(file.Environments))
			for n := range file.Environments {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Errorf("--backends-env %q is not declared in backends.yaml (available: %s)",
				opts.BackendsEnv, strings.Join(names, ", "))
		}
		envName = opts.BackendsEnv
	} else {
		envName, _, _ = backends.DetectEnvironment(file)
	}
	eff := backends.Effective(file, envName, target)

	if opts.ArtifactStore == nil && eff.Cache != nil {
		store, err := storeurl.OpenArtifactStoreFromSpec(ctx, *eff.Cache)
		if err != nil {
			return fmt.Errorf("cache backend: %w", err)
		}
		opts.ArtifactStore = store
	}
	if opts.LogStore == nil && eff.Logs != nil {
		store, err := storeurl.OpenLogStoreFromSpec(ctx, *eff.Logs)
		if err != nil {
			return fmt.Errorf("logs backend: %w", err)
		}
		opts.LogStore = store
	}
	return nil
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
