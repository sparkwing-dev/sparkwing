package orchestrator

import (
	"os"
	goruntime "runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
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

func defaultLocalLabels() []string {
	return []string{
		"local",
		"os=" + goruntime.GOOS,
		"arch=" + goruntime.GOARCH,
	}
}
