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

func describeArgs(ctx context.Context, process ExecutorProcess, cfg SessionConfig, pipeline string, args map[string]string, masker *secrets.Masker) (PreparedPlanning, bool, error) {
	limit := cmp.Or(cfg.DescribeTimeout, 30*time.Second)
	bounded, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var raw json.RawMessage
	if err := ask(bounded, process, nodeproto.Request{Op: nodeproto.OpDescribe}, &raw); err != nil {
		class := "error"
		var structured *apierr.Error
		switch {
		case ctx.Err() == nil && bounded.Err() != nil:
			class = fmt.Sprintf("no reply within %s", limit)
		case errors.As(err, &structured) && structured.Code != "":
			class = string(structured.Code)
		}
		return PreparedPlanning{}, false, maskedRunError{err: err, text: "describe failed: " + class + " (details withheld: secrets not yet known)"}
	}
	if masker == nil {
		masker = secrets.NewMasker()
	}
	prepared, err := (SDKPlanner{Masker: masker}).describe(raw, PlanningProcessInput{Entrypoint: pipeline, Args: args})
	var structured *apierr.Error
	known := err == nil || errors.As(err, &structured) && structured.Code == apierr.UnknownPipeline
	return prepared, known, err
}

func releaseDescribed(output *processOutput, known bool, err error) error {
	if !known {
		output.withhold("describe failed before secrets were known")
	} else {
		output.release()
	}
	return err
}

func planRun(ctx context.Context, process ExecutorProcess, validation nodeproto.ValidationContext, pipeline string, args map[string]string, rc nodeproto.RunContext) (nodeproto.PlanDoc, []byte, error) {
	var raw json.RawMessage
	if err := ask(ctx, process, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: args, Run: &rc}, &raw); err != nil {
		return nodeproto.PlanDoc{}, nil, err
	}
	doc, err := nodeproto.ValidatePlan(raw, validation)
	return doc, raw, err
}

func ask(ctx context.Context, process ExecutorProcess, req nodeproto.Request, result any) error {
	req.ID = "host"
	reply, err := process.Request(ctx, req)
	if err != nil {
		return err
	}
	return reply.Decode(result)
}

func describedSecrets(description nodeproto.PipelineDescription, required bool) []string {
	var names []string
	for _, secret := range description.Secrets {
		if !required || !secret.Optional {
			names = append(names, secret.Name)
		}
	}
	return names
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
