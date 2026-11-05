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

// perf: suitesDone closes once the full Go and Postgres suites release their
// cores; a nil channel keeps the race queue at its initial width.
func runRaceTouched(ctx context.Context, mode gateMode, suitesDone <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, raceTouchedStepTimeout)
	defer cancel()
	files, scope, err := gateScope(ctx, mode, "Go file(s)", existingGoFiles)
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
	// safety: this job has no frontend-deps step of its own, and hosted CI installs web deps only for the gate lane.
	if slices.Contains(targets["."], "./tests/acceptance") {
		if err := ensureFrontendDeps(ctx); err != nil {
			return err
		}
	}
	return withGoTestScratch(func(testRoot string) error {
		return withProductTestHome(func(home string) error {
			return raceModules(ctx, targets, testRoot, home, suitesDone)
		})
	})
}

func raceModules(ctx context.Context, targets map[string][]string, testRoot, home string, suitesDone <-chan struct{}) error {
	var failures []string
	for _, module := range mapKeys(targets) {
		pkgs := slices.Clone(targets[module])
		var sharded []racePackagePlan
		if module == "." && gateRaceParallelism(currentHost()) > 0 {
			pkgs = slices.DeleteFunc(pkgs, func(pkg string) bool {
				if gateRaceShardsForPackage(pkg) > 0 {
					sharded = append(sharded, racePackagePlan{pkg: pkg, shardCount: gateRaceShardsForPackage(pkg), lead: gateRaceLeadTests(pkg)})
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
			workers := raceWorkers{initial: gateRaceParallelism(currentHost()), extra: gateRaceExtraWorkers(currentHost()), ready: suitesDone}
			if runErr := runRaceShardsAtHome(ctx, home, module, sharded, workers, gateRaceShardArgs); runErr != nil {
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

// perf: on the four-core runner the race queue shares the box with the full
// and Postgres suites, which hold two cores; once they finish, race takes all four.
func gateRaceExtraWorkers(h hostShape) int {
	if h.cpus == overlappedGoSuiteCPUs {
		return h.cpus - gateRaceParallelism(h)
	}
	return 0
}

func gateRaceShardsForPackage(pkg string) int {
	switch pkg {
	case "./internal/controller":
		return 6
	case "./tests/acceptance":
		return 3
	case "./internal/nodehost", "./cmd/sparkwing":
		return 2
	case "./internal/wingd/localcontroller":
		return 1
	default:
		return 0
	}
}

// perf: measured single-thread race long poles. TestStorage takes about 4.5 minutes,
// a third of tests/acceptance under race; the controller pair takes 1 to 2 minutes each.
func gateRaceLeadTests(pkg string) []string {
	switch pkg {
	case "./internal/controller":
		return []string{"TestLogTreeTraversesEveryTenantDescendantProperty", "TestRetryHistoryTraversesLongChainsAndCycles"}
	case "./tests/acceptance":
		return []string{"TestStorage"}
	default:
		return nil
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
