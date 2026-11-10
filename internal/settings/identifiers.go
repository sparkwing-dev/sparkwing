package settings

import (
	"fmt"
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// VisitDeclarationIdentifiers covers retained checkout layers even when precedence hides a value.
func VisitDeclarationIdentifiers(declarations runspec.CheckoutDeclarations, visit func(value, path string) error) error {
	for _, layer := range []struct {
		name  string
		value runspec.CheckoutLayer
	}{
		{"pipeline", declarations.Pipeline}, {"defaults", declarations.Defaults},
	} {
		path := "declarations." + layer.name
		if err := visitLabels(layer.value.Requires.Value, layer.value.Prefers.Value, path, visit); err != nil {
			return err
		}
		if err := visit(layer.value.Profile.Value, path+".profile"); err != nil {
			return err
		}
	}
	return nil
}

// VisitExecutionIdentifiers includes resolved label winners in the existing identifier boundary.
func VisitExecutionIdentifiers(execution runspec.ExecutionSettings, visit func(value, path string) error) error {
	if err := visitLabels(execution.Options.Requires.Value, execution.Options.Prefers.Value, "settings", visit); err != nil {
		return err
	}
	if err := visit(execution.Options.Profile.Value, "settings.profile"); err != nil {
		return err
	}
	for i, id := range slices.Sorted(maps.Keys(execution.Jobs)) {
		job := execution.Jobs[id]
		if err := visitLabels(job.Requires.Value, job.Prefers.Value, fmt.Sprintf("settings.jobs[%d]", i), visit); err != nil {
			return err
		}
	}
	return nil
}

func visitLabels(requires, prefers []string, path string, visit func(value, path string) error) error {
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
