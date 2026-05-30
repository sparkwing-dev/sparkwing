package orchestrator

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

// laptopOnlySourceTypes lists source backends that only work when
// the run dispatches to a local in-process runner. macos-keychain
// is unambiguously laptop-only (shells out to /usr/bin/security on
// darwin); file is laptop-only because the controller pod doesn't
// carry the user's dotenv on its filesystem. env is intentionally
// excluded -- cluster pods can legitimately have env vars set via
// Kubernetes Secret mounts.
var laptopOnlySourceTypes = map[string]bool{
	sources.TypeFile: true,
}

// validateSourceRunnerPortability rejects runs whose pipeline
// dispatch binds to a laptop-only source backend when dispatch will
// go to a non-local runner. Fires before any node executes so the
// failure mode is the same as a missing source.
//
// Local runner detection: the in-process runner (the default) is
// the only known-local kind today. A nil opts.Runner falls back to
// NewInProcessRunner inside Run(), so nil is also local. Cluster
// pool / k8s / static runners report any non-nil, non-InProcess
// type and trigger the guard.
func validateSourceRunnerPortability(opts Options, active runner.Runner) error {
	if opts.PipelineYAML == nil || opts.PipelineYAML.Dispatch == nil || opts.PipelineYAML.Dispatch.Source == nil {
		return nil
	}
	src := opts.PipelineYAML.Dispatch.Source
	if !laptopOnlySourceTypes[src.Type] {
		return nil
	}
	if isLocalRunner(active) {
		return nil
	}
	return fmt.Errorf(
		"pipeline %q binds to source %s (type: %s), which is laptop-only; "+
			"this run dispatches to a non-local runner that can't reach it. "+
			"Choose a type=profile source for cluster targets, or run on a local runner",
		opts.Pipeline, src.Describe(), src.Type,
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
