package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type queuedTestPackage struct {
	module, pkg, phase string
	commands           []string
	admitted           bool
}

type queuedTestLane struct {
	capacity, runtimeWidth int
	packages               []*queuedTestPackage
}

func queuedTestPackages(plans []affectedTestPlan, allowance int) ([]*queuedTestPackage, [][]queuedTestLane) {
	var packages []*queuedTestPackage
	var groups [][]queuedTestLane
	for _, modules := range affectedShortModules(plans, allowance) {
		var group []queuedTestLane
		for _, module := range modules {
			p := module.plan
			ordinary, width, patterns := p.shortAllocation(module.allowance)
			lane := queuedTestLane{capacity: width, runtimeWidth: width}
			for _, pkg := range ordinary {
				item := &queuedTestPackage{module: p.module, pkg: pkg, phase: "short", commands: []string{shortTestCommand(width, 1, []string{pkg}, "")}}
				packages = append(packages, item)
				lane.packages = append(lane.packages, item)
			}
			if len(lane.packages) > 0 {
				group = append(group, lane)
			}
			if len(patterns) > 0 {
				item := &queuedTestPackage{module: p.module, pkg: storeTestPackage, phase: "short"}
				for _, pattern := range patterns {
					item.commands = append(item.commands, shortTestCommand(1, 1, []string{storeTestPackage}, pattern))
				}
				packages = append(packages, item)
				group = append(group, queuedTestLane{capacity: len(patterns), runtimeWidth: 1, packages: []*queuedTestPackage{item}})
			}
		}
		groups = append(groups, group)
	}
	for _, p := range plans {
		lane := queuedTestLane{capacity: allowance, runtimeWidth: allowance}
		for _, pkg := range mapKeys(p.critical) {
			args := criticalTestArgs(p.critical, []string{pkg})
			item := &queuedTestPackage{module: p.module, pkg: pkg, phase: "critical", commands: []string{goCommandWithLimits(allowance, 1, "test", args)}}
			packages = append(packages, item)
			lane.packages = append(lane.packages, item)
		}
		if len(lane.packages) > 0 {
			groups = append(groups, []queuedTestLane{lane})
		}
	}
	return packages, groups
}

func runQueuedTestPackages(ctx context.Context, plans []affectedTestPlan, host hostShape, home string, timings *goTestTimings) (string, error) {
	allowance := goStepParallelism(host)
	packages, groups := queuedTestPackages(plans, allowance)
	if len(packages) == 0 {
		return "", nil
	}
	for _, group := range groups {
		total := 0
		for _, lane := range group {
			total += lane.capacity
			for _, item := range lane.packages {
				if len(item.commands) < 1 || len(item.commands) > lane.capacity {
					return "", fmt.Errorf("queued affected tests: package %s needs %d command slots, lane capacity %d", item.pkg, len(item.commands), lane.capacity)
				}
			}
			sparkwing.Info(ctx, "queued affected tests: %s module %s lane capacity %d, GOMAXPROCS=%d and go test -p 1 per command", lane.packages[0].phase, lane.packages[0].module, lane.capacity, lane.runtimeWidth)
		}
		if total > allowance {
			return "", fmt.Errorf("queued affected tests: group needs %d command slots, allowance %d", total, allowance)
		}
	}
	var mu sync.Mutex
	firstFailure := ""
	var observed []string
	stop := func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		if firstFailure == "" {
			firstFailure = reason
		}
	}
	timings.failed = func(ev goTestEvent) {
		pkg := ev.Package
		if pkg == "" {
			pkg = ev.ImportPath
		}
		reason := strings.TrimSpace(ev.Action + " " + pkg + " " + ev.Test)
		mu.Lock()
		defer mu.Unlock()
		if firstFailure == "" {
			firstFailure = reason
		}
		observed = append(observed, reason)
	}
	defer func() { timings.failed = nil }()
	type result struct {
		item   *queuedTestPackage
		lane   int
		errors []error
	}
	finished := make(chan result, allowance)
	var failures []string
	for _, group := range groups {
		next := make([]int, len(group))
		activeLane := make([]int, len(group))
		active := 0
		for {
			for laneIndex, lane := range group {
				for next[laneIndex] < len(lane.packages) {
					item := lane.packages[next[laneIndex]]
					weight := len(item.commands)
					if activeLane[laneIndex]+weight > lane.capacity || active+weight > allowance {
						break
					}
					mu.Lock()
					if ctx.Err() != nil && firstFailure == "" {
						firstFailure = ctx.Err().Error()
					}
					if firstFailure != "" {
						mu.Unlock()
						break
					}
					next[laneIndex]++
					item.admitted = true
					active += weight
					activeLane[laneIndex] += weight
					mu.Unlock()
					// safety: all shards belong to the admitted package, so a sibling
					// failure cannot suppress any of its selected bodies.
					go func() {
						errs := make([]error, len(item.commands))
						var running sync.WaitGroup
						for i, command := range item.commands {
							running.Go(func() {
								script := productTestScript(fmt.Sprintf("cd %q && GOFLAGS=%s %s", item.module, shellQuote(affectedTestGoFlags), command), home)
								errs[i] = timings.run(ctx, script)
								if errs[i] != nil {
									stop(fmt.Sprintf("%s %s: %v", item.phase, item.pkg, errs[i]))
								}
							})
						}
						running.Wait()
						finished <- result{item: item, lane: laneIndex, errors: errs}
					}()
				}
			}
			if active == 0 {
				break
			}
			completed := <-finished
			weight := len(completed.item.commands)
			active -= weight
			activeLane[completed.lane] -= weight
			for _, err := range completed.errors {
				if err != nil {
					failures = append(failures, describeModuleFailure(completed.item.module, err))
				}
			}
		}
		if firstFailure != "" {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		stop(err.Error())
	}
	for _, p := range plans {
		sparkwing.Info(ctx, "affected tests: %s", p.describe())
	}
	if firstFailure == "" {
		return "", nil
	}
	summary := queuedTestCutoffSummary(ctx, packages, firstFailure, observed, plans, host)
	if len(failures) == 0 {
		return summary, fmt.Errorf("queued affected tests observed failure: %s\n  - %s", firstFailure, strings.Join(observed, "\n  - "))
	}
	return summary, fmt.Errorf("go test failed in %d command(s):\n  - %s", len(failures), strings.Join(failures, "\n  - "))
}

func queuedTestCutoffSummary(ctx context.Context, packages []*queuedTestPackage, firstFailure string, observed []string, plans []affectedTestPlan, host hostShape) string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "**Queued affected tests stopped admitting packages**\n\nFirst observed failure: `%s`. All admitted package commands finished and their output drained.\n\n", firstFailure)
	if len(observed) > 0 {
		summary.WriteString("Observed failure events:\n\n")
		for _, failure := range observed {
			fmt.Fprintf(&summary, "- `%s`\n", failure)
		}
		summary.WriteString("\n")
	}
	for _, phase := range []string{"short", "critical"} {
		fmt.Fprintf(&summary, "Skipped %s packages:\n\n", phase)
		count := 0
		for _, item := range packages {
			if item.admitted || item.phase != phase {
				continue
			}
			count++
			fmt.Fprintf(&summary, "- `%s`: `%s`\n", item.module, item.pkg)
			sparkwing.Info(ctx, "affected tests: skipped %s package %s: %s", phase, item.module, item.pkg)
		}
		if count == 0 {
			summary.WriteString("None.\n")
		}
		summary.WriteString("\n")
	}
	summary.WriteString("**Local rerun: all selected work**\n\nThe original batched commands run every selected short and critical test, collect all failures and share one private product-test home.\n\n```sh\n")
	var commands strings.Builder
	commands.WriteString("rerun_status=0\n")
	for _, group := range affectedTestGroups(plans, goStepParallelism(host)) {
		for _, command := range group {
			fmt.Fprintf(&commands, "env -C %s GOWORK=auto GOFLAGS=%s %s || rerun_status=1\n", shellQuote(filepath.Join(sparkwing.WorkDir(), command.module)), shellQuote(affectedTestGoFlags), command.command)
		}
	}
	commands.WriteString(`exit "$rerun_status"`)
	bindings := productTestPins("")
	bindings[0] = productTestHomeVar + `="$rerun_home"`
	script := withoutInherited(withPinned(commands.String(), bindings), productTestUnset)
	fmt.Fprintf(&summary, "(rerun_home=$(mktemp -d) || exit; trap 'rm -rf -- \"$rerun_home\"' EXIT; cd %s || exit;\n%s\n)\n", shellQuote(sparkwing.WorkDir()), script)
	summary.WriteString("```\n")
	return summary.String()
}
