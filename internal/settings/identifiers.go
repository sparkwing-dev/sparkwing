package settings

import (
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"maps"
	"slices"
)

// VisitExecutionIdentifiers includes resolved label winners in the existing identifier boundary.
func VisitExecutionIdentifiers(execution runspec.ExecutionSettings, visit func(value, path string) error) error {
	labels := func(requires, prefers []string, path string) error {
		for _, list := range []struct {
			name   string
			values []string
		}{{"requires", requires}, {"prefers", prefers}} {
			for i, value := range list.values {
				if err := visit(value, fmt.Sprintf("%s.%s[%d]", path, list.name, i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := labels(execution.Options.Requires.Value, execution.Options.Prefers.Value, "settings"); err != nil {
		return err
	}
	for i, id := range slices.Sorted(maps.Keys(execution.Jobs)) {
		job := execution.Jobs[id]
		if err := labels(job.Requires.Value, job.Prefers.Value, fmt.Sprintf("settings.jobs[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}
