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
	"path/filepath"
	"sort"

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
	// v0.6 removed the per-pipeline targets block; --target is no
	// longer a flag. Leave this completer in place as a no-op so any
	// stale shell completion script referencing the verb doesn't
	// error -- it just emits an empty list.
	_ = p
	return nil
}

// runInternalCompleteRunners emits runner names from the project's
// sparkwing.yaml runners section, including the implicit "local" entry.
func runInternalCompleteRunners(_ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	if !ok {
		return nil
	}
	cfg, err := projectconfig.Load(filepath.Join(sparkwingDir, projectconfig.Filename))
	if err != nil || cfg == nil {
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	names := []string{"local"}
	for n := range cfg.Runners {
		if n != "local" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
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
