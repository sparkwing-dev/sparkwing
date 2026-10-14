package nodehost

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type describeDoc struct {
	Protocol  string `json:"protocol"`
	Pipelines []struct {
		Name string `json:"name"`
		Args []struct {
			Name    string `json:"name"`
			Default string `json:"default"`
			Secret  bool   `json:"secret"`
		} `json:"args"`
	} `json:"pipelines"`
}

// safety: describe runs before the run row exists, so an entry that speaks
// another protocol or lacks the pipeline is refused without recording a run.
// The value of every flag describe marks secret, and the default of every
// secret flag of every pipeline described, is registered with masker, when
// set, as soon as describe answers; known reports that it did. Until then the
// secrets are unknown, so a describe that fails returns its class without its
// text.
func describeArgs(ctx context.Context, s *Session, pipeline string, args map[string]string, masker *secrets.Masker) (merged map[string]string, known bool, err error) {
	var doc describeDoc
	// safety: no run row or node timeout exists yet, so without this bound a
	// process that stops answering holds the run open forever.
	limit := cmp.Or(s.cfg.DescribeTimeout, 30*time.Second)
	dctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if err := ask(dctx, s, nodeproto.Request{Op: nodeproto.OpDescribe}, &doc); err != nil {
		class := "error"
		var perr *nodeproto.Error
		switch {
		case ctx.Err() == nil && dctx.Err() != nil:
			class = fmt.Sprintf("no reply within %s", limit)
		case errors.As(err, &perr) && perr.Code != "":
			class = string(perr.Code)
		}
		return nil, false, maskedRunError{err: err, text: "describe failed: " + class + " (details withheld: secrets not yet known)"}
	}
	// safety: describe plans every pipeline the entry registers, and any of
	// them may print its own secret defaults.
	for _, p := range doc.Pipelines {
		for _, a := range p.Args {
			if a.Secret && masker != nil {
				masker.Register(a.Default)
			}
		}
	}
	if doc.Protocol != nodeproto.Version {
		return nil, true, mismatch("describe", doc.Protocol)
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
		for _, a := range p.Args {
			if a.Secret && masker != nil && merged[a.Name] != "" {
				masker.Register(merged[a.Name])
			}
		}
		return merged, true, nil
	}
	return nil, true, &nodeproto.Error{Code: nodeproto.CodeUnknownPipeline, Message: fmt.Sprintf("the pipeline entry has no pipeline %q", pipeline)}
}

// safety: held output reaches the sink only once describe's secrets are
// known, and err is masked here since the run row that masks later errors
// does not exist yet.
func releaseDescribed(s *Session, known bool, masker *secrets.Masker, err error) error {
	if !known {
		s.WithholdOutput("describe failed before secrets were known")
		return err
	}
	s.ReleaseOutput()
	if err != nil && masker != nil {
		err = maskedRunError{err: err, text: masker.Mask(err.Error())}
	}
	return err
}

func planRun(ctx context.Context, s *Session, pipeline string, args map[string]string, rc nodeproto.RunContext) (nodeproto.PlanDoc, []byte, error) {
	var raw json.RawMessage
	var doc nodeproto.PlanDoc
	err := ask(ctx, s, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: args, Run: &rc}, &raw)
	if err == nil {
		raw, err = nodeproto.StampSpecHashes(raw)
	}
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
		if err := validateSteps(n); err != nil {
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
	sources := map[string]string{}
	for _, n := range nodes {
		if n.Closures != nil {
			for _, id := range n.Closures.Generator {
				sources[id] = n.ID
			}
		}
	}
	for _, n := range nodes {
		for _, g := range n.NeedsGenerated {
			if _, ok := sources[g]; !ok {
				return invalid("node %s needs the nodes of generator %s, which the plan does not hold", n.ID, g)
			}
		}
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
		next := waitsOn(byID[id], byID)
		for _, g := range byID[id].NeedsGenerated {
			next = append(next, sources[g])
		}
		for _, d := range next {
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
