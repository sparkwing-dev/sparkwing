package sparkwing

import (
	"context"
	"strings"
)

// RunnerInfo describes the runner that's about to execute (or is
// executing) the current job. Populated by the orchestrator at
// dispatch; accessed by job bodies via sparkwing.Runner(ctx).
//
// Adapters that need a single typed signal for "am I local?" or
// "am I in a Kubernetes pod?" should branch on the runner that
// actually got picked rather than sniff the environment:
//
//	r := sparkwing.Runner(ctx)
//	if r.HasLabel("local") {
//	    return &kubectlClient{kubeconfig: kubeconfigPath()}, nil
//	}
//	return &apiClient{}, nil
//
// The type lives in the SDK (not pkg/runners) so step bodies don't
// need to import the config-file parser. The field set is a
// subset of pkg/runners.Runner trimmed to what step bodies care
// about: identity + capability labels.
type RunnerInfo struct {
	// Name is the runner identifier as declared in runners.yaml
	// (e.g. "local", "cloud-linux", "mac-mini"). Empty when the
	// active runner hasn't been named -- treat as a synonym for
	// "the implicit runner of this dispatch venue."
	Name string

	// Type is the runner kind. Same vocabulary as runners.yaml's
	// type: field -- "local", "kubernetes", "static". Empty when
	// the orchestrator couldn't classify the active runner.
	Type string

	// Labels are the equality strings the runner advertises.
	// Same shape as runners.yaml's labels: list and runner.LabelAdvertiser
	// AdvertisedLabels.
	Labels []string
}

// HasLabel reports whether the runner advertises the given label
// term. Honors the comma-OR within-a-term syntax used by
// Job.Requires / Prefers / WhenRunner:
//
//	r.HasLabel("os=linux")          // single label
//	r.HasLabel("os=linux,os=macos") // OR within term
//
// Empty receiver returns false rather than panicking so adapters
// can call Runner(ctx).HasLabel("...") unconditionally on a
// possibly-nil result (e.g. inside Plan(), where no runner is
// installed yet).
func (r *RunnerInfo) HasLabel(term string) bool {
	if r == nil || term == "" {
		return false
	}
	if !strings.ContainsRune(term, ',') {
		t := strings.TrimSpace(term)
		for _, l := range r.Labels {
			if l == t {
				return true
			}
		}
		return false
	}
	for _, alt := range strings.Split(term, ",") {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		for _, l := range r.Labels {
			if l == alt {
				return true
			}
		}
	}
	return false
}

type runnerCtxKey struct{}

// Runner returns the RunnerInfo the orchestrator installed for the
// current job. Returns nil when no orchestrator has installed one
// (inside Plan(), in unit tests that haven't called WithRunner,
// in code paths that don't carry the dispatch context).
//
// Adapters branching on the result should treat nil and an
// unconfigured runner the same way the local default does -- the
// "Writing adapters" section of the execution-model design has the
// pattern.
func Runner(ctx context.Context) *RunnerInfo {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(runnerCtxKey{}).(*RunnerInfo)
	return v
}
