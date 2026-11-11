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
}

func queuedTestPackages(plans []affectedTestPlan, allowance int) ([]queuedTestPackage, int) {
	var packages []queuedTestPackage
	for _, p := range plans {
		for _, pkg := range p.packages {
			command := goCommandWithLimits(allowance, 1, "test", "-short -timeout "+productGoTestTimeout+" "+shellQuote(pkg))
			packages = append(packages, queuedTestPackage{p.module, pkg, "short", []string{command}})
		}
	}
	shortEnd := len(packages)
	for _, p := range plans {
		for _, pkg := range mapKeys(p.critical) {
			args := criticalTestArgs(p.critical, []string{pkg})
			packages = append(packages, queuedTestPackage{p.module, pkg, "critical", []string{goCommandWithLimits(allowance, 1, "test", args)}})
		}
	}
	return packages, shortEnd
}

func runQueuedTestPackages(ctx context.Context, plans []affectedTestPlan, host hostShape, home string, timings *goTestTimings) (string, error) {
	allowance := goStepParallelism(host)
	packages, shortEnd := queuedTestPackages(plans, allowance)
	if len(packages) == 0 {
		return "", nil
	}
	for _, item := range packages {
		if len(item.commands) < 1 || len(item.commands) > allowance {
			return "", fmt.Errorf("queued affected tests: package %s needs %d command slots, allowance %d", item.pkg, len(item.commands), allowance)
		}
	}
	sparkwing.Info(ctx, "queued affected tests: package admission allowance %d; GOMAXPROCS=%d and go test -p 1 per command", allowance, allowance)
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
		index  int
		errors []error
	}
	finished := make(chan result, allowance)
	next, active := 0, 0
	var failures []string
	for next < len(packages) || active > 0 {
		for next < len(packages) {
			item := packages[next]
			weight := len(item.commands)
			if active+weight > allowance || (next == shortEnd && active > 0) {
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
			index := next
			next++
			active += weight
			mu.Unlock()
			// safety: all shards belong to the admitted package, so a sibling failure
			// cannot suppress any of its selected bodies.
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
				finished <- result{index, errs}
			}()
		}
		if active == 0 {
			break
		}
		completed := <-finished
		item := packages[completed.index]
		active -= len(item.commands)
		for _, err := range completed.errors {
			if err != nil {
				failures = append(failures, describeModuleFailure(item.module, err))
			}
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
	summary := queuedTestCutoffSummary(ctx, packages, next, firstFailure, observed, plans, host)
	if len(failures) == 0 {
		return summary, fmt.Errorf("queued affected tests observed failure: %s\n  - %s", firstFailure, strings.Join(observed, "\n  - "))
	}
	return summary, fmt.Errorf("go test failed in %d command(s):\n  - %s", len(failures), strings.Join(failures, "\n  - "))
}

func queuedTestCutoffSummary(ctx context.Context, packages []queuedTestPackage, admitted int, firstFailure string, observed []string, plans []affectedTestPlan, host hostShape) string {
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
		for _, item := range packages[admitted:] {
			if item.phase != phase {
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
	for _, p := range plans {
		for _, command := range p.commands(host) {
			fmt.Fprintf(&commands, "env -C %s GOWORK=auto GOFLAGS=%s %s || rerun_status=1\n", shellQuote(filepath.Join(sparkwing.WorkDir(), p.module)), shellQuote(affectedTestGoFlags), command)
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
