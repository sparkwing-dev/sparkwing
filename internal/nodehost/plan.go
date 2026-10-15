package nodehost

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// safety: describe runs before the run row exists, so invalid entries leave no run.
// Secret flag values and defaults must be registered before diagnostics can be logged.
// Until describe succeeds, secrets are unknown and failures expose only their class.
func describeArgs(ctx context.Context, s *Session, pipeline string, args map[string]string, masker *secrets.Masker) (merged map[string]string, known bool, err error) {
	var doc nodeproto.DescribeDoc
	// safety: no run row or node timeout exists yet, so without this bound a
	// process that stops answering holds the run open forever.
	limit := cmp.Or(s.cfg.DescribeTimeout, 30*time.Second)
	dctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if err := ask(dctx, s, nodeproto.Request{Op: nodeproto.OpDescribe}, &doc); err != nil {
		class := "error"
		var perr *apierr.Error
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
				masker.Register(value(a.Default))
			}
		}
	}
	for _, p := range doc.Pipelines {
		if p.Name != pipeline {
			continue
		}
		s.secrets = nil
		s.requiredSecrets = nil
		for _, secret := range p.Secrets {
			s.secrets = append(s.secrets, secret.Name)
			if !secret.Optional {
				s.requiredSecrets = append(s.requiredSecrets, secret.Name)
			}
		}
		merged := make(map[string]string, len(args)+len(p.Args))
		for _, a := range p.Args {
			if a.Default != nil {
				merged[a.Name] = *a.Default
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
	return nil, true, &apierr.Error{Code: apierr.UnknownPipeline, Message: fmt.Sprintf("the pipeline entry has no pipeline %q", pipeline)}
}

// safety: held output reaches the sink only once describe's secrets are known.
func releaseDescribed(s *Session, known bool, err error) error {
	if !known {
		s.WithholdOutput("describe failed before secrets were known")
		return err
	}
	s.ReleaseOutput()
	return err
}

func planRun(ctx context.Context, s *Session, pipeline string, args map[string]string, rc nodeproto.RunContext) (nodeproto.PlanDoc, []byte, error) {
	var raw json.RawMessage
	var doc nodeproto.PlanDoc
	err := ask(ctx, s, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: args, Run: &rc}, &raw)
	if err != nil {
		return doc, nil, err
	}
	doc, err = nodeproto.ValidatePlan(raw, s.cfg.Validation)
	return doc, raw, err
}

func ask(ctx context.Context, s *Session, req nodeproto.Request, result any) error {
	reply, err := s.Call(ctx, req)
	if err != nil {
		return err
	}
	return reply.Decode(result)
}

func invalid(format string, args ...any) error {
	return &apierr.Error{Code: apierr.PlanInvalid, Message: fmt.Sprintf(format, args...)}
}

// safety: a node or closure id the host cannot resolve unambiguously would
// let one node's eval or run land on another, so the run fails before any
// node starts.
func validateNodes(nodes []nodeproto.JobSpec, validation nodeproto.ValidationContext) error {
	raw, err := json.Marshal(nodeproto.PlanDoc{Jobs: nodes})
	if err != nil {
		return err
	}
	_, err = nodeproto.ValidatePlan(raw, validation)
	return err
}

// safety: an optional dependency the plan does not hold is no edge at all,
// and an on_failure node waits on its parent alone, as the dispatcher does.
func waitsOn(n nodeproto.JobSpec, plan map[string]nodeproto.JobSpec) []string {
	if n.OnFailureOf != "" {
		return []string{n.OnFailureOf}
	}
	waits := append([]string{}, n.Needs...)
	for _, d := range n.NeedsIfPresent {
		if _, ok := plan[d]; ok {
			waits = append(waits, d)
		}
	}
	return waits
}
