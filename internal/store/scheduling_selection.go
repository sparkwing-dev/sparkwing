package store

import (
	"fmt"
	"path"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// PlanSelectionSkips excludes jobs outside the selected dependency closure.
func PlanSelectionSkips(doc nodeproto.PlanDoc, pattern, start, stop string) (map[string]string, error) {
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
	keep, err := planDependencyClosure(doc, matched)
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
