package nodehost

import (
	"fmt"
	"path"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func onlySkips(doc nodeproto.PlanDoc, pattern, start, stop string) (map[string]string, error) {
	if pattern == "" {
		return nil, nil
	}
	if start != "" || stop != "" {
		return nil, fmt.Errorf("--only is mutually exclusive with --start-at / --stop-at")
	}
	var ids, matched []string
	for _, n := range doc.Jobs {
		ids = append(ids, n.ID)
		ok, err := path.Match(pattern, n.ID)
		if err != nil {
			return nil, fmt.Errorf("--only=%q: %w", pattern, err)
		}
		if ok {
			matched = append(matched, n.ID)
		}
	}
	if len(doc.Jobs) == 0 {
		return nil, nil
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("--only=%q matched no jobs (declared: %v)", pattern, ids)
	}
	keep, err := store.PlanDependencyClosure(doc, matched)
	if err != nil {
		return nil, err
	}
	skip := map[string]string{}
	for _, n := range doc.Jobs {
		if !keep[n.ID] && n.OnFailureOf == "" {
			skip[n.ID] = fmt.Sprintf("outside --only=%q", pattern)
		}
	}
	return skip, nil
}

func selectionSkips(doc nodeproto.PlanDoc, options runspec.Options) (map[string]string, error) {
	if err := validateStepRange(doc, options.StartAt.Value, options.StopAt.Value); err != nil {
		return nil, err
	}
	return onlySkips(doc, options.Only.Value, options.StartAt.Value, options.StopAt.Value)
}
