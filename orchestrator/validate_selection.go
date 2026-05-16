package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// validateTargetSelection enforces the --for contract: pipelines
// with no declared targets reject the flag outright; declared
// targets require --for to name one of them. Empty --for on a
// single-target pipeline is permitted -- the resolver auto-picks.
func validateTargetSelection(opts Options) error {
	if opts.PipelineYAML == nil {
		return nil
	}
	targets := opts.PipelineYAML.TargetNames()
	switch {
	case len(targets) == 0:
		if opts.Target != "" {
			return fmt.Errorf("pipeline %q does not declare any targets; --for is not applicable",
				opts.Pipeline)
		}
	default:
		if opts.Target == "" {
			return nil
		}
		if !opts.PipelineYAML.HasTarget(opts.Target) {
			return fmt.Errorf("pipeline %q has no target %q; declared: %v",
				opts.Pipeline, opts.Target, targets)
		}
	}
	return nil
}

// validateJobOverrides confirms each --job entry names a real plan
// node and a runner declared in runners.yaml whose labels satisfy
// the job's Requires terms. Runs after Plan() so node ids are
// resolved but before dispatch so the run record never reflects an
// unreachable selection.
func validateJobOverrides(opts Options, plan *sparkwing.Plan) error {
	if len(opts.JobRunnerOverrides) == 0 {
		return nil
	}
	nodes := plan.Nodes()
	byID := make(map[string]*sparkwing.JobNode, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		byID[n.ID()] = n
		ids = append(ids, n.ID())
	}
	for id, runnerName := range opts.JobRunnerOverrides {
		node, ok := byID[id]
		if !ok {
			sort.Strings(ids)
			suggest := sparkwing.SuggestClosest(id, ids)
			if suggest != "" {
				return fmt.Errorf("--job %s=%s: no plan node named %q; did you mean %q?",
					id, runnerName, id, suggest)
			}
			return fmt.Errorf("--job %s=%s: no plan node named %q (declared: %v)",
				id, runnerName, id, ids)
		}
		r, ok, err := runners.Resolve(opts.SparkwingDir, runnerName)
		if err != nil {
			return fmt.Errorf("--job %s=%s: resolve runner: %w", id, runnerName, err)
		}
		if !ok {
			return fmt.Errorf("--job %s=%s: runner %q is not declared in runners.yaml",
				id, runnerName, runnerName)
		}
		if reqs := node.RequiresLabels(); len(reqs) > 0 {
			if !sparkwing.MatchLabels(reqs, r.Labels) {
				return fmt.Errorf("--job %s=%s: runner %q (labels %v) does not satisfy job %q Requires %v",
					id, runnerName, runnerName, r.Labels, id, reqs)
			}
		}
	}
	return nil
}

// laptopOnlySourceTypes lists source backends that only work when
// the run dispatches to a local in-process runner. macos-keychain
// is unambiguously laptop-only (shells out to /usr/bin/security on
// darwin); file is laptop-only because the controller pod doesn't
// carry the user's dotenv on its filesystem. env is intentionally
// excluded -- cluster pods can legitimately have env vars set via
// Kubernetes Secret mounts.
var laptopOnlySourceTypes = map[string]bool{
	sources.TypeFile:          true,
	sources.TypeMacosKeychain: true,
}

// validateSourceRunnerPortability rejects runs whose target binds
// to a laptop-only source backend when dispatch will go to a
// non-local runner. Fires before any node executes so the failure
// mode is the same as a missing source.
//
// Local runner detection: the in-process runner (the default) is
// the only known-local kind today. A nil opts.Runner falls back to
// NewInProcessRunner inside Run(), so nil is also local. Cluster
// pool / k8s / static runners report any non-nil, non-InProcess
// type and trigger the guard.
func validateSourceRunnerPortability(opts Options, active runner.Runner) error {
	if opts.PipelineYAML == nil || opts.Target == "" {
		return nil
	}
	t, ok := opts.PipelineYAML.Targets[opts.Target]
	if !ok || t.Source == "" {
		return nil
	}
	src, ok, err := sources.Resolve(opts.SparkwingDir, t.Source)
	if err != nil || !ok {
		// Missing source surfaces via the normal resolver path; we
		// only police portability when we actually have a source.
		return nil
	}
	if !laptopOnlySourceTypes[src.Type] {
		return nil
	}
	if isLocalRunner(active) {
		return nil
	}
	return fmt.Errorf(
		"target %q binds to source %q (type: %s), which is laptop-only; "+
			"this run dispatches to a non-local runner that can't reach it. "+
			"Choose a remote-controller source for cluster targets, or run without --on",
		opts.Target, src.Name, src.Type,
	)
}

// isLocalRunner reports whether the active runner runs the job
// in the same process as the orchestrator. Used to gate the
// laptop-only source guard: only non-local runners trip the check.
func isLocalRunner(r runner.Runner) bool {
	if r == nil {
		return true
	}
	if _, ok := r.(*InProcessRunner); ok {
		return true
	}
	if adv, ok := r.(runner.LabelAdvertiser); ok {
		for _, l := range adv.AdvertisedLabels() {
			if l == "local" || strings.HasPrefix(l, "local=") {
				return true
			}
		}
	}
	return false
}
