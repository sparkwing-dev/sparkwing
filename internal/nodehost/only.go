package nodehost

import (
	"context"
	"fmt"
	"path"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: --sw-only keeps the matched nodes and what they depend on; OnFailure recovery nodes and nodes a
// generator adds later are never cut, as in the dispatcher.
func onlySkips(doc nodeproto.PlanDoc, pattern, start, stop string) (map[string]string, error) {
	if pattern == "" {
		return nil, nil
	}
	if start != "" || stop != "" {
		return nil, fmt.Errorf("--only is mutually exclusive with --start-at / --stop-at")
	}
	byID := make(map[string]nodeproto.PlanNode, len(doc.Nodes))
	keep := map[string]bool{}
	var ids, matched []string
	for _, n := range doc.Nodes {
		byID[n.ID] = n
		ids = append(ids, n.ID)
		ok, err := path.Match(pattern, n.ID)
		if err != nil {
			return nil, fmt.Errorf("--only=%q: %w", pattern, err)
		}
		if ok {
			matched = append(matched, n.ID)
		}
	}
	if len(doc.Nodes) == 0 {
		return nil, nil
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("--only=%q matched no jobs (declared: %v)", pattern, ids)
	}
	for len(matched) > 0 {
		id := matched[len(matched)-1]
		matched = matched[:len(matched)-1]
		if n, ok := byID[id]; ok && !keep[id] {
			keep[id] = true
			matched = append(matched, n.Deps...)
		}
	}
	skip := map[string]string{}
	for _, n := range doc.Nodes {
		if !keep[n.ID] && n.OnFailureOf == "" {
			skip[n.ID] = fmt.Sprintf("outside --only=%q", pattern)
		}
	}
	return skip, nil
}

// safety: the cap sits inside admission and group waits, so a node waiting there holds no worker, as the
// dispatcher released its slot; inline nodes run uncapped as before.
func workersLayer(cfg *Config, next Executor) Executor {
	if cfg.Workers <= 0 {
		return next
	}
	sem := make(chan struct{}, cfg.Workers)
	return func(ctx context.Context, n *NodeRun) Result {
		if m := n.Plan.Modifiers; m != nil && m.Inline {
			return next(ctx, n)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return Result{Outcome: sparkwing.Cancelled, Err: context.Cause(ctx)}
		}
		defer func() { <-sem }()
		return next(ctx, n)
	}
}
