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
	files, scope, err := changeScope(ctx, "Go file(s)", goSourceFiles)
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
			packages := targets[module]
			sparkwing.Info(ctx, "race-touched: %s: %s", module, strings.Join(packages, " "))
			cmd := boundedGoCommand(runtime.NumCPU(), "test", "-race -count=1 -timeout 30m "+strings.Join(packages, " "))
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
	for _, file := range files {
		if isTestdataPath(file) {
			continue
		}
		directory := filepath.ToSlash(filepath.Dir(file))
		module := owningModule(directory, modules)
		if module == "" {
			continue
		}
		pattern := "./"
		if directory != module {
			pattern += strings.TrimPrefix(directory, module+"/")
		}
		if seen[module] == nil {
			seen[module] = map[string]bool{}
		}
		seen[module][pattern] = true
	}
	output := make(map[string][]string, len(seen))
	for module, packages := range seen {
		output[module] = mapKeys(packages)
	}
	return output
}

func owningModule(directory string, modules []string) string {
	best := ""
	for _, module := range modules {
		module = filepath.ToSlash(module)
		if module != "." && directory != module && !strings.HasPrefix(directory, module+"/") {
			continue
		}
		if len(module) > len(best) || best == "" {
			best = module
		}
	}
	return best
}

func mapKeys[V any](module map[string]V) []string {
	keys := make([]string, 0, len(module))
	for k := range module {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
