package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type describeDoc struct {
	Protocol  string `json:"protocol"`
	Pipelines []struct {
		Name string `json:"name"`
		Args []struct {
			Name    string `json:"name"`
			Default string `json:"default"`
		} `json:"args"`
	} `json:"pipelines"`
}

// safety: describe runs before the run row exists, so an entry that speaks
// another protocol or lacks the pipeline is refused without recording a run.
func describeArgs(ctx context.Context, s *Session, pipeline string, args map[string]string) (map[string]string, error) {
	var doc describeDoc
	if err := ask(ctx, s, nodeproto.Request{Op: nodeproto.OpDescribe}, &doc); err != nil {
		return nil, err
	}
	if doc.Protocol != nodeproto.Version {
		return nil, mismatch("describe", doc.Protocol)
	}
	for _, p := range doc.Pipelines {
		if p.Name != pipeline {
			continue
		}
		merged := make(map[string]string, len(args)+len(p.Args))
		for _, a := range p.Args {
			if a.Default != "" {
				merged[a.Name] = a.Default
			}
		}
		for k, v := range args {
			merged[k] = v
		}
		return merged, nil
	}
	return nil, &nodeproto.Error{Code: nodeproto.CodeUnknownPipeline, Message: fmt.Sprintf("the pipeline entry has no pipeline %q", pipeline)}
}

func planRun(ctx context.Context, s *Session, pipeline string, args map[string]string, rc nodeproto.RunContext) (nodeproto.PlanDoc, []byte, error) {
	var raw json.RawMessage
	var doc nodeproto.PlanDoc
	err := ask(ctx, s, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: args, Run: &rc}, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	switch {
	case err != nil:
		return doc, nil, err
	case doc.Protocol != nodeproto.Version:
		return doc, nil, mismatch("plan", doc.Protocol)
	}
	return doc, raw, validateNodes(doc.Nodes)
}

func ask(ctx context.Context, s *Session, req nodeproto.Request, result any) error {
	reply, err := s.Call(ctx, req)
	if err != nil {
		return err
	}
	return reply.Decode(result)
}

func mismatch(doc, got string) error {
	return &nodeproto.Error{Code: nodeproto.CodeProtocolMismatch, Message: fmt.Sprintf(
		"the %s document speaks node protocol %q and this engine %q; rebuild .sparkwing against a matching SDK", doc, got, nodeproto.Version)}
}

func invalid(format string, args ...any) error {
	return &nodeproto.Error{Code: nodeproto.CodePlanInvalid, Message: fmt.Sprintf(format, args...)}
}

// safety: a node or closure id the host cannot resolve unambiguously would
// let one node's eval or run land on another, so the run fails before any
// node starts.
func validateNodes(nodes []nodeproto.PlanNode) error {
	byID := make(map[string]nodeproto.PlanNode, len(nodes))
	closures := map[string]string{}
	claim := func(owner string, c *nodeproto.PlanClosures) error {
		if c == nil {
			return nil
		}
		ids := append(append(append(append([]string{c.Verify, c.CacheKey}, c.SkipIf...), c.BeforeRun...), c.AfterRun...), c.Generator...)
		for _, id := range ids {
			if id == "" {
				continue
			}
			if prior, dup := closures[id]; dup {
				return invalid("closure id %s belongs to both %s and %s", id, prior, owner)
			}
			closures[id] = owner
		}
		return nil
	}
	for _, n := range nodes {
		if n.ID == "" {
			return invalid("the plan holds a node with no id")
		}
		if _, dup := byID[n.ID]; dup {
			return invalid("the plan holds node %s twice", n.ID)
		}
		byID[n.ID] = n
		if err := claim("node "+n.ID, n.Closures); err != nil {
			return err
		}
		if n.Work == nil {
			continue
		}
		for _, st := range n.Work.Steps {
			if err := claim("step "+n.ID+"/"+st.ID, st.Closures); err != nil {
				return err
			}
		}
	}
	for _, n := range nodes {
		for _, d := range n.Deps {
			if _, ok := byID[d]; !ok {
				return invalid("node %s needs %s, which the plan does not hold", n.ID, d)
			}
		}
		if _, ok := byID[n.OnFailureOf]; n.OnFailureOf != "" && !ok {
			return invalid("node %s recovers %s, which the plan does not hold", n.ID, n.OnFailureOf)
		}
	}
	const visiting, visited = 1, 2
	state := make(map[string]int, len(nodes))
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case visited:
			return nil
		case visiting:
			return invalid("the plan has a dependency cycle %s", strings.Join(append(path, id), " -> "))
		}
		state[id] = visiting
		for _, d := range waitsOn(byID[id], byID) {
			if err := visit(d, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	for _, n := range nodes {
		if err := visit(n.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// safety: an optional dependency the plan does not hold is no edge at all,
// and an on_failure node waits on its parent alone, as the dispatcher does.
func waitsOn(n nodeproto.PlanNode, plan map[string]nodeproto.PlanNode) []string {
	if n.OnFailureOf != "" {
		return []string{n.OnFailureOf}
	}
	waits := append([]string{}, n.Deps...)
	for _, d := range n.OptionalDeps {
		if _, ok := plan[d]; ok {
			waits = append(waits, d)
		}
	}
	return waits
}
