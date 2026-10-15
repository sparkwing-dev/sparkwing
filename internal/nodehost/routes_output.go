package nodehost

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func (g *grant) output(ctx context.Context, job string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	if !validNodeID(job) {
		return nil, executorError(apierr.BadRequest, "invalid job id")
	}
	if job == g.NodeID && g.Output != nil {
		return g.Output, nil
	}
	if g.Claim == nil && g.Local != nil {
		return g.localOutput(ctx, job)
	}
	if g.Claim == nil {
		return nil, serviceUnavailable()
	}
	var row struct {
		Status  string `json:"status"`
		Outcome string `json:"outcome"`
	}
	status, _, err := g.Claim.call(ctx, http.MethodGet, runPath(g.RunID, "nodes", job), nil, &row)
	if status == http.StatusNotFound {
		return nil, executorError(apierr.UnknownJob, "run has no such job")
	}
	if err != nil || status != http.StatusOK {
		return nil, serviceUnavailable()
	}
	if row.Status != "done" {
		return nil, routeFailure(apierr.Conflict, apierr.ReasonNotReady, "job has not finished")
	}
	if row.Outcome != string(sparkwing.Success) && row.Outcome != string(sparkwing.Memoized) {
		return nil, routeFailure(apierr.NotFound, apierr.ReasonNoOutput, "job finished without output")
	}
	var output store.OutputReadGrant
	status, code, err := g.Claim.call(ctx, http.MethodGet, runPath(g.RunID, "nodes", job, "output"), nil, &output)
	if status == http.StatusForbidden && code == "output_not_ancestor" {
		return nil, routeFailure(apierr.Forbidden, apierr.ReasonNotDependency, "job output is not a dependency")
	}
	if err != nil || status != http.StatusOK {
		return nil, serviceUnavailable()
	}
	return g.Claim.output(ctx, output)
}

func (g *grant) latest(ctx context.Context, pipeline, job string, age time.Duration) (ProcessOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	if !validName(pipeline) || !validNodeID(job) {
		return ProcessOutput{}, executorError(apierr.BadRequest, "invalid pipeline or job")
	}
	if g.Claim == nil && g.Local != nil {
		return g.localLatest(ctx, pipeline, job, age)
	}
	if g.Claim == nil {
		return ProcessOutput{}, serviceUnavailable()
	}
	var in store.ClaimInput
	status, code, err := g.Claim.call(ctx, http.MethodPost, runPath(g.RunID, "nodes", g.NodeID, "claim", "input"), store.ClaimInputRequest{Kind: store.ClaimInputLastRun, Pipeline: pipeline, Node: job, MaxAgeMS: age.Milliseconds()}, &in)
	if status == http.StatusNotFound {
		return ProcessOutput{}, executorError(apierr.NotFound, "no successful pipeline run matches")
	}
	if status == http.StatusForbidden && code == "input_undeclared" {
		return ProcessOutput{}, routeFailure(apierr.Forbidden, apierr.ReasonReferenceUndeclared, "pipeline reference is not declared")
	}
	if err != nil || status != http.StatusOK {
		return ProcessOutput{}, serviceUnavailable()
	}
	if in.Output.URL == "" {
		return ProcessOutput{}, routeFailure(apierr.NotFound, apierr.ReasonNoOutput, "latest run recorded no output")
	}
	raw, err := g.Claim.output(ctx, in.Output)
	return ProcessOutput{RunID: in.RunID, Output: raw}, err
}
