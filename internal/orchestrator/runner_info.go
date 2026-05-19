package orchestrator

import (
	"os"
	goruntime "runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// runnerInfoFor builds the sparkwing.RunnerInfo the orchestrator
// installs on the per-node ctx. Reads advertised labels from the
// active runner when it implements LabelAdvertiser; classifies the
// runner type (local vs kubernetes vs static) from the concrete
// type and label hints.
//
// The pod path (RunNodeOnce) also dispatches through an
// InProcessRunner -- "local in process" relative to the pod -- but
// the operator-visible runner is kubernetes/static. podRunnerInfo
// overrides the type and name from env vars the trigger loop
// stamps onto the pod.
func runnerInfoFor(r runner.Runner) *sparkwing.RunnerInfo {
	if r == nil {
		return &sparkwing.RunnerInfo{
			Name:   "local",
			Type:   "local",
			Labels: defaultLocalLabels(),
		}
	}
	info := &sparkwing.RunnerInfo{}
	if adv, ok := r.(runner.LabelAdvertiser); ok {
		info.Labels = adv.AdvertisedLabels()
	}
	if _, ok := r.(*InProcessRunner); ok {
		info.Type = "local"
		if info.Name == "" {
			info.Name = "local"
		}
	}
	// Heuristic for non-InProcess runners with advertised labels:
	// classify by the first label that looks like a runner type.
	if info.Type == "" {
		for _, l := range info.Labels {
			switch l {
			case "kubernetes", "static":
				info.Type = l
			}
			if info.Type != "" {
				break
			}
		}
	}
	return info
}

// podRunnerInfo returns the RunnerInfo a runner pod should expose
// to job bodies. SPARKWING_RUNNER_NAME / SPARKWING_RUNNER_TYPE /
// SPARKWING_RUNNER_LABELS (comma-separated) are stamped by the
// cluster trigger loop; defaults fill in when only Type is set.
//
// Returns nil when the env carries no runner identity at all -- a
// fallback for laptop-tested pod-path invocations where the trigger
// loop hasn't been involved.
func podRunnerInfo() *sparkwing.RunnerInfo {
	name := strings.TrimSpace(os.Getenv("SPARKWING_RUNNER_NAME"))
	typ := strings.TrimSpace(os.Getenv("SPARKWING_RUNNER_TYPE"))
	labelsRaw := strings.TrimSpace(os.Getenv("SPARKWING_RUNNER_LABELS"))
	if name == "" && typ == "" && labelsRaw == "" {
		return nil
	}
	if typ == "" {
		typ = "kubernetes"
	}
	labels := make([]string, 0)
	if labelsRaw != "" {
		for _, l := range strings.Split(labelsRaw, ",") {
			l = strings.TrimSpace(l)
			if l != "" {
				labels = append(labels, l)
			}
		}
	}
	return &sparkwing.RunnerInfo{Name: name, Type: typ, Labels: labels}
}

// defaultLocalLabels mirrors pkg/runners.implicitLocal so a
// laptop-default RunnerInfo advertises the same OS/arch markers a
// runners.yaml-declared "local" entry would.
func defaultLocalLabels() []string {
	return []string{
		"local",
		"os=" + goruntime.GOOS,
		"arch=" + goruntime.GOARCH,
	}
}
