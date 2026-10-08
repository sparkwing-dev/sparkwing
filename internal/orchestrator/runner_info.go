package orchestrator

import (
	"os"
	goruntime "runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
	if _, ok := r.(*NodeExecutor); ok {
		info.Type = "local"
		if info.Name == "" {
			info.Name = "local"
		}
	}
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

func podRunnerInfo() *sparkwing.RunnerInfo {
	name := strings.TrimSpace(os.Getenv("SPARKWING_RUNNER_NAME"))
	typ := strings.TrimSpace(os.Getenv("SPARKWING_RUNNER_TYPE"))
	labelsRaw := strings.TrimSpace(os.Getenv("SPARKWING_RUNNER_LABELS"))
	if name == "" && typ == "" && labelsRaw == "" {
		return nil
	}
	var labels []string
	if labelsRaw != "" {
		labels = sparkwingruntime.NormalizeLabels(strings.Split(labelsRaw, ","))
	}
	return &sparkwing.RunnerInfo{Name: name, Type: typ, Labels: labels}
}

// safety: the dispatcher's labels are its own configuration, so they are trusted
// as they stand; the check is static, so a busy runner never skips a WhenRunner
// job for want of a slot.
func dispatcherMatches(selector, advertised []string) bool {
	return match.Evaluate(match.Profile{Labels: advertised}, match.Demand{Selector: selector}).OK()
}

func defaultLocalLabels() []string {
	return []string{
		"local",
		"os=" + goruntime.GOOS,
		"arch=" + goruntime.GOARCH,
	}
}
