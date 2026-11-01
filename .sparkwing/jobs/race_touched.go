package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	raceTouchedStepTimeout    = gateRunTimeout
	raceTouchedPackageTimeout = raceTouchedStepTimeout * 3 / 4
)

func runRaceTouched(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, raceTouchedStepTimeout)
	defer cancel()
	files, scope, err := changeScope(ctx, "Go file(s)", existingGoFiles)
	if err != nil {
		return err
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	targets, deferred := raceTargetsForGate(touchedPackageTargets(files, modules))
	sparkwing.Info(ctx, "race-touched: %s; step budget %s, package timeout %s", scope, raceTouchedStepTimeout, raceTouchedPackageTimeout)
	for _, pkg := range deferred {
		sparkwing.Info(ctx, "race-touched: %s races at the release boundary, not here; pre-release covers it", pkg)
	}
	if len(targets) == 0 {
		sparkwing.Info(ctx, "race-touched: no package changed; nothing to race-test")
		return nil
	}
	return withGoTestScratch(func(testRoot string) error {
		return withProductTestHome(func(home string) error {
			return raceModules(ctx, targets, testRoot, home)
		})
	})
}

func raceModules(ctx context.Context, targets map[string][]string, testRoot, home string) error {
	var failures []string
	for _, module := range mapKeys(targets) {
		pkgs := slices.Clone(targets[module])
		var sharded []racePackagePlan
		if module == "." && gateRaceParallelism(currentHost()) > 0 {
			pkgs = slices.DeleteFunc(pkgs, func(pkg string) bool {
				if gateRaceShardsForPackage(pkg) > 0 {
					sharded = append(sharded, racePackagePlan{pkg, gateRaceShardsForPackage(pkg)})
					return true
				}
				return false
			})
		}
		sparkwing.Info(ctx, "race-touched: %s: %s", module, strings.Join(targets[module], " "))
		cmd := raceGoCommand(currentHost(), "-race -count=1 -timeout "+raceTouchedPackageTimeout.String()+" "+strings.Join(pkgs, " "))
		if len(pkgs) > 0 {
			script := productTestScript(fmt.Sprintf("cd %q && %s", module, cmd), home)
			if _, runErr := sparkwing.Bash(ctx, script).Env("TMPDIR", testRoot).Run(); runErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", module, runErr))
			}
		}
		if len(sharded) > 0 {
			if runErr := runRaceShardsAtHome(ctx, home, module, sharded, gateRaceParallelism(currentHost()), gateRaceShardArgs); runErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", module, runErr))
			}
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("go test -race failed:\n  - %s", strings.Join(failures, "\n  - "))
}

func gateRaceParallelism(h hostShape) int {
	if h.cpus == overlappedGoSuiteCPUs {
		return 2
	}
	if parallelism := goStepParallelism(h); parallelism >= 2 {
		return parallelism
	}
	return 0
}

func gateRaceShardsForPackage(pkg string) int {
	switch pkg {
	case "./internal/controller":
		return 4
	case "./internal/nodehost", "./cmd/sparkwing":
		return 2
	case "./internal/wingd/localcontroller":
		return 1
	default:
		return 0
	}
}

func gateRaceShardArgs(shard []string) []string {
	return []string{"-test.run", raceTestPattern(shard), "-test.count=1", "-test.timeout=" + raceTouchedPackageTimeout.String()}
}

// safety: the hosted store suite exceeded 80 minutes in each of four shards.
// Pre-release owns its race coverage, including the short-class restriction.
const gateDeferredRaceTarget = "./internal/store"

func raceTargetsForGate(targets map[string][]string) (map[string][]string, []string) {
	kept := make(map[string][]string, len(targets))
	var deferred []string
	for module, pkgs := range targets {
		keep := make([]string, 0, len(pkgs))
		for _, pkg := range pkgs {
			if pkg == gateDeferredRaceTarget {
				deferred = append(deferred, pkg)
				continue
			}
			keep = append(keep, pkg)
		}
		if len(keep) > 0 {
			kept[module] = keep
		}
	}
	sort.Strings(deferred)
	return kept, deferred
}

func raceGoCommand(h hostShape, args string) string {
	// perf: four-core runners can overlap two package processes while each Go
	// runtime retains the existing GOMAXPROCS=1 bound.
	if h.cpus == overlappedGoSuiteCPUs {
		return goCommandWithLimits(1, 2, "test", args)
	}
	return boundedGoCommand(h, "test", args)
}

func touchedPackageTargets(files, modules []string) map[string][]string {
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
