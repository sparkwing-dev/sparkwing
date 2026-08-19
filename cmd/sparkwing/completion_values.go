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
)

// runInternalCompleteTargets remains callable by completion scripts
// generated before pipeline targets were removed.
func runInternalCompleteTargets(_ []string) error {
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
