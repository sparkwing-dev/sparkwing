package orchestrator

import (
	"os"
	goruntime "runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

func defaultLocalLabels() []string {
	return []string{
		"local",
		"os=" + goruntime.GOOS,
		"arch=" + goruntime.GOARCH,
	}
}
