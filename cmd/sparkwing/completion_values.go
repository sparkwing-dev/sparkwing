// Value-completion helpers introduced by the execution-model CLI:
// --for, --job, --prefer, --backends-env, --default-runner.
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
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/runners"
	"github.com/sparkwing-dev/sparkwing/profile"
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
		return nil //nolint:nilerr
	}
	_, cfg, err := pipelines.Discover(cwd)
	if err != nil || cfg == nil {
		return nil //nolint:nilerr
	}
	p := cfg.Find(args[0])
	if p == nil {
		return nil
	}
	for _, name := range p.TargetNames() {
		fmt.Println(name)
	}
	return nil
}

// runInternalCompleteRunners emits runner names from runners.yaml
// (merged with the user overlay), including the implicit "local"
// entry when neither file declares it.
func runInternalCompleteRunners(_ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	if !ok {
		return nil
	}
	names, err := runners.Names(sparkwingDir)
	if err != nil {
		return nil //nolint:nilerr
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

// runInternalCompleteRunnerLabels emits the union of advertised
// labels across runners.yaml entries (and, when the pipeline argument
// is provided and pipelines.yaml declares a runners: allow-list, the
// intersection of those runners with the resolved set). Useful for
// --prefer completion.
func runInternalCompleteRunnerLabels(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	if !ok {
		return nil
	}
	names, err := runners.Names(sparkwingDir)
	if err != nil {
		return nil //nolint:nilerr
	}

	allowed := map[string]bool{}
	if len(args) == 1 && args[0] != "" {
		if _, cfg, derr := pipelines.Discover(cwd); derr == nil && cfg != nil {
			if p := cfg.Find(args[0]); p != nil && len(p.Runners) > 0 {
				for _, r := range p.Runners {
					allowed[r] = true
				}
			}
		}
	}

	seen := map[string]struct{}{}
	for _, n := range names {
		if len(allowed) > 0 && !allowed[n] {
			continue
		}
		r, ok, rerr := runners.Resolve(sparkwingDir, n)
		if rerr != nil || !ok {
			continue
		}
		for _, l := range r.Labels {
			if l == "" {
				continue
			}
			seen[l] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	for _, l := range out {
		fmt.Println(l)
	}
	return nil
}

// runInternalCompleteBackendsEnvs emits the environments declared in
// backends.yaml, including the built-in gha and kubernetes detect
// rules every install gets for free.
func runInternalCompleteBackendsEnvs(_ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr
	}
	sparkwingDir, _ := walkUpForSparkwing(cwd)
	file, err := backends.ResolveWithEnv(sparkwingDir)
	if err != nil {
		return nil //nolint:nilerr
	}
	names := file.EnvironmentOrder()
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

// runInternalCompleteProfilesForPipeline emits profile names whose
// EffectiveDefaultRunner sits in the pipeline's resolved runner
// allow-list (pipelines.yaml runners: intersected with runners.yaml).
// Falls back to the full profile list when the pipeline is unknown
// or declares no runner allow-list -- the operator still gets to
// pick freely, no filtering surprises.
func runInternalCompleteProfilesForPipeline(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return runInternalCompleteProfiles(nil)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return runInternalCompleteProfiles(nil)
	}
	path, err := profile.DefaultPath()
	if err != nil {
		return nil //nolint:nilerr
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil //nolint:nilerr
	}
	_, pcfg, _ := pipelines.Discover(cwd)
	var allowed map[string]bool
	if pcfg != nil {
		if p := pcfg.Find(args[0]); p != nil && len(p.Runners) > 0 {
			allowed = map[string]bool{}
			for _, r := range p.Runners {
				allowed[r] = true
			}
		}
	}
	if len(allowed) == 0 {
		// No filter: emit everything, matching the existing default.
		for _, n := range cfg.Names() {
			fmt.Println(n)
		}
		return nil
	}
	for _, name := range cfg.Names() {
		p, err := profile.Resolve(cfg, name)
		if err != nil {
			continue
		}
		eff := strings.TrimSpace(p.EffectiveDefaultRunner())
		if eff == "" || allowed[eff] {
			fmt.Println(name)
		}
	}
	return nil
}
