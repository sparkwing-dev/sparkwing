package nodehost

import (
	"context"
	"encoding/json"
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
	byID := make(map[string]nodeproto.JobSpec, len(doc.Jobs))
	keep := map[string]bool{}
	var ids, matched []string
	for _, n := range doc.Jobs {
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
	if len(doc.Jobs) == 0 {
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
			matched = append(matched, n.Needs...)
		}
	}
	skip := map[string]string{}
	for _, n := range doc.Jobs {
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
		if n.Plan.Inline {
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

// safety: a controller-dispatched run has no host to record skips, so --sw-only cuts the nodes from the plan the
// controller accepts; a recovery node goes with its cut parent, since nothing it recovers can run.
func cutOutsideOnly(doc nodeproto.PlanDoc, raw []byte, pattern string) (nodeproto.PlanDoc, []byte, error) {
	skips, err := onlySkips(doc, pattern, "", "")
	if err != nil || len(skips) == 0 {
		return doc, raw, err
	}
	keep := func(n nodeproto.JobSpec) bool {
		_, cut := skips[n.ID]
		_, parentCut := skips[n.OnFailureOf]
		return !cut && (n.OnFailureOf == "" || !parentCut)
	}
	var fields map[string]json.RawMessage
	var nodes []json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return doc, nil, err
	}
	if err := json.Unmarshal(fields["jobs"], &nodes); err != nil {
		return doc, nil, err
	}
	if len(nodes) != len(doc.Jobs) {
		return doc, nil, fmt.Errorf("--only: the plan lists %d nodes, its document %d", len(nodes), len(doc.Jobs))
	}
	var keptRaw []json.RawMessage
	var kept []nodeproto.JobSpec
	for i, n := range doc.Jobs {
		if keep(n) {
			kept, keptRaw = append(kept, n), append(keptRaw, nodes[i])
		}
	}
	list, err := json.Marshal(keptRaw)
	if err != nil {
		return doc, nil, err
	}
	fields["jobs"] = list
	if raw, err = json.Marshal(fields); err != nil {
		return doc, nil, err
	}
	doc.Jobs = kept
	return doc, raw, nil
}
