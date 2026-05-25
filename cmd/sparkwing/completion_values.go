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
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
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
	for _, name := range p.TargetNames() {
		fmt.Println(name)
	}
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
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	_, pcfg, _ := projectconfig.DiscoverPipelines(cwd)
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
		p, _, err := profile.Resolve(name, "", cfg)
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
