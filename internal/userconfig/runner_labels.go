package userconfig

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// IgnoredLabel is a runner.labels entry that names a fact Sparkwing grants, which matching never reads.
type IgnoredLabel struct {
	File  string
	Line  int
	Label string
}

// String names the file, line and label and says the label has no effect.
func (l IgnoredLabel) String() string {
	return fmt.Sprintf("%s: runner.labels: line %d: %q names a fact Sparkwing grants (name, class, location, team, pool or local) and has no effect; remove it", l.File, l.Line, l.Label)
}

// IgnoredRunnerLabels lists the granted-fact labels in the machine file at path, which placement drops.
func IgnoredRunnerLabels(path string) ([]IgnoredLabel, error) {
	runner, err := Node(path, Runner)
	if err != nil || runner == nil {
		return nil, err
	}
	labels := YAMLField(runner, "labels")
	if labels == nil {
		return nil, nil
	}
	var ignored []IgnoredLabel
	for _, item := range labels.Content {
		label := strings.TrimSpace(item.Value)
		if label != "" && !strings.HasPrefix(label, "sparkwing-") && len(match.SelfAsserted([]string{label})) == 0 {
			ignored = append(ignored, IgnoredLabel{File: path, Line: item.Line, Label: label})
		}
	}
	return ignored, nil
}
