// Value-completion helpers for --for and --default-runner.
//
// Each helper prints one entry per line to stdout and exits zero.
// The shell wrappers in completion.go feed them into _describe.
// Silent failure (empty output, exit 0) is the contract: if the
// underlying file can't be read, the menu falls back to "no values"
// rather than spamming an error during completion.
package main

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// runInternalCompleteTargets emits the pipeline's declared targets.
// Silent when no pipelines.yaml is on the path or the pipeline isn't
// listed; the completion menu falls back to nothing.
func runInternalCompleteTargets(args []string) error {
	if len(args) != 1 {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	_, cfg, err := projectconfig.DiscoverPipelines(cwd)
	if err != nil || cfg == nil {
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	p := cfg.Find(args[0])
	if p == nil {
		return nil
	}
	_ = p
	return nil
}

// runInternalCompleteRunners is retained as a no-op so any stale
// shell completion script referencing the verb stays callable. The
// pre-v0.6 runners: registry in sparkwing.yaml was dropped; runner
// selection now happens via job-level Requires() labels and there's
// nothing to enumerate from the repo's YAML.
func runInternalCompleteRunners(_ []string) error {
	fmt.Println("local")
	return nil
}

// runInternalCompleteProfilesForPipeline emits the full profile
// list. The pre-v0.6 version filtered to profiles whose
// EffectiveDefaultRunner sat in the pipeline's resolved runner
// allow-list, but default_runner is gone -- the unfiltered list is
// the honest completion now.
func runInternalCompleteProfilesForPipeline(args []string) error {
	_ = args
	return runInternalCompleteProfiles(nil)
}
