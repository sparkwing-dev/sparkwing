package orchestrator

import (
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
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
