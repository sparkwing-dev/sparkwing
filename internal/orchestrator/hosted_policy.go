package orchestrator

import (
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func guardCheck(pipeline string, rules pipelines.Guards, at pipelines.GuardContext, defaultBranch func() string) func(map[string]string) error {
	if rules.IsEmpty() {
		return nil
	}
	var needs string
	for _, raw := range append(slices.Clone(rules.Reject), rules.Require...) {
		if tok, err := pipelines.ParseGuardToken(raw); err == nil && tok.Kind == pipelines.KindGitBranchOnDef {
			needs = raw
		}
	}
	return func(args map[string]string) error {
		if needs != "" && at.GitDefaultBranch == "" {
			if at.GitDefaultBranch = defaultBranch(); at.GitDefaultBranch == "" {
				return fmt.Errorf("pipeline %q: guard %q could not resolve the repository's default branch, so the run is refused", pipeline, needs)
			}
		}
		at.Args = args
		return rules.Evaluate(pipeline, at)
	}
}

// PipelineRequires reads the pipeline or project placement requirements from the checkout.
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

func protocolTriggerSource(recorded string) string {
	switch recorded {
	case "schedule", "push", "webhook":
		return recorded
	case "github":
		return "webhook"
	}
	return "manual"
}
