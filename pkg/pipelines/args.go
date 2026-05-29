package pipelines

import (
	"fmt"
	"sort"
)

// PipelineArgs is the v0.6 typed-args block declared on a pipeline.
// Outer key is the arg name (matches the CLI flag form, e.g.
// "target"); inner key is the arg's allowed value (e.g. "prod").
// Inner value is the [Target] binding: runners, source, secrets,
// approvals, etc. for runs that select that arg value.
//
// In v0.6 only the well-known arg name "target" is schema-bearing
// (binds runners/source/secrets); future bind names will land here
// as the SDK grows. See pipelines.Pipeline.Args.
type PipelineArgs map[string]map[string]Target

// Target returns the "target" arg's value map, or nil when no
// target arg is declared. Mirror accessor for callers that want the
// v0.5-shaped Targets map without manual lookup.
func (a PipelineArgs) Target() map[string]Target {
	if a == nil {
		return nil
	}
	return a["target"]
}

// EffectiveTargets returns the pipeline's targets, preferring the
// v0.6 args.target block when set and falling back to the legacy
// top-level targets: block for back-compat. Callers downstream
// (orchestrator, CLI, etc.) consume this rather than reading the
// raw fields so the migration sits in one place.
//
// A pipeline that declares BOTH args.target AND targets: is rejected
// by [Pipeline.ValidateArgs]; this accessor doesn't re-check (the
// caller already validated).
func (p *Pipeline) EffectiveTargets() map[string]Target {
	if t := p.Args.Target(); t != nil {
		return t
	}
	return p.Targets
}

// ValidateArgs rejects pipeline-args shapes that the loader can't
// reconcile: both args.target AND top-level targets: declared, or
// an unknown bind name in args (only "target" is supported in v0.6).
// Returns nil when the args block is consistent.
//
// Called by projectconfig.Load (and back-fed from any direct
// pipeline-yaml unmarshaller) so per-file parsing surfaces the
// failure mode close to where the bad config was authored.
func (p *Pipeline) ValidateArgs() error {
	if p == nil {
		return nil
	}
	if len(p.Targets) > 0 && p.Args.Target() != nil {
		return fmt.Errorf("pipeline %q: declares both top-level `targets:` (legacy) and `args.target:` (v0.6); remove one (prefer args.target -- see docs/migrations/v0.6.0.md)", p.Name)
	}
	if len(p.Args) > 0 {
		var unknown []string
		for name := range p.Args {
			if name != "target" {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf(
				"pipeline %q: args block declares unknown bind name(s) [%v]; v0.6 only supports `target` -- future schema-bearing binds will land in later releases",
				p.Name, unknown,
			)
		}
	}
	return nil
}

// HasLegacyTargets reports whether the pipeline still uses the
// top-level `targets:` shape (rather than the v0.6 args.target).
// Used by the migration-warning path to log a deprecation notice
// once per loaded pipeline.
func (p *Pipeline) HasLegacyTargets() bool {
	return p != nil && len(p.Targets) > 0 && p.Args.Target() == nil
}
