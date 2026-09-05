package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runRaceTouched(ctx context.Context) error {
	files, scope, err := changeScope(ctx, "Go file(s)", existingGoFiles)
	if err != nil {
		return err
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	targets := raceTargets(files, modules)
	sparkwing.Info(ctx, "race-touched: %s", scope)
	if len(targets) == 0 {
		sparkwing.Info(ctx, "race-touched: no package changed; nothing to race-test")
		return nil
	}
	return withGoTestScratch(func(testRoot string) error {
		var failures []string
		for _, module := range mapKeys(targets) {
			pkgs := targets[module]
			sparkwing.Info(ctx, "race-touched: %s: %s", module, strings.Join(pkgs, " "))
			// safety: go test's default 10-minute budget is per package binary and
			// pkg/store under the race detector outlives it on a one-core hosted
			// runner; the pipeline's own timeout still bounds the step.
			cmd := boundedGoCommand(runtime.NumCPU(), "test", "-race -count=1 -timeout 30m "+strings.Join(pkgs, " "))
			script := withoutInherited(fmt.Sprintf("cd %q && %s", module, cmd), productTestUnset)
			if _, runErr := sparkwing.Bash(ctx, script).Env("TMPDIR", testRoot).Run(); runErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", module, runErr))
			}
		}
		if len(failures) == 0 {
			return nil
		}
		return fmt.Errorf("go test -race failed in %d module(s):\n  - %s",
			len(failures), strings.Join(failures, "\n  - "))
	})
}

func raceTargets(files, modules []string) map[string][]string {
	seen := map[string]map[string]bool{}
	for _, f := range files {
		if isTestdataPath(f) {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(f))
		module := owningModule(dir, modules)
		if module == "" {
			continue
		}
		pattern := "./"
		if dir != module {
			pattern += strings.TrimPrefix(dir, module+"/")
		}
		if seen[module] == nil {
			seen[module] = map[string]bool{}
		}
		seen[module][pattern] = true
	}
	out := make(map[string][]string, len(seen))
	for module, pkgs := range seen {
		out[module] = mapKeys(pkgs)
	}
	return out
}

func owningModule(dir string, modules []string) string {
	best := ""
	for _, m := range modules {
		m = filepath.ToSlash(m)
		if m != "." && dir != m && !strings.HasPrefix(dir, m+"/") {
			continue
		}
		if len(m) > len(best) || best == "" {
			best = m
		}
	}
	return best
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
