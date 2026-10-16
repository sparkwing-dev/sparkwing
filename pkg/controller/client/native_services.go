package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// NativeProcessClient uses a retained controller award for one process's service operations.
type NativeProcessClient struct {
	transport *engineClient
	run, job  string
	request   runspec.Request
}

// ProcessClient binds a process declaration to its independently authenticated parent.
func (c *LocalHostClient) ProcessClient(ctx context.Context, principal capability.Principal, job string) (*NativeProcessClient, error) {
	transport, err := c.nativeTransport(ctx, principal)
	if err != nil {
		return nil, err
	}
	if job == "" || principal.Capability.Unit == capability.UnitNode && principal.Capability.Binding.NodeID != job {
		return nil, localHostClientRefusal()
	}
	snapshot, err := c.ReadSnapshot(ctx, c.run)
	if err != nil {
		return nil, err
	}
	return &NativeProcessClient{transport: transport, run: c.run, job: job, request: snapshot.Run.Request.Request}, nil
}

// Annotation commits a masked annotation through the common reporting transaction.
func (c *NativeProcessClient) Annotation(ctx context.Context, input nodeproto.AnnotationRequest) error {
	return c.transport.AppendAnnotation(ctx, c.run, c.job, controllerwire.AnnotationWrite{
		Annotation: controllerwire.Annotation{Level: input.Level, Message: input.Message, File: input.File, Line: input.Line, Column: input.Column, EndLine: input.EndLine},
		StepID:     input.Step,
	})
}

// Summary commits a masked summary through the common reporting transaction.
func (c *NativeProcessClient) Summary(ctx context.Context, input nodeproto.SummaryRequest) error {
	suffix := "/summary"
	if input.Step != "" {
		suffix = "/steps" + suffix
	}
	return c.transport.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}"+suffix, "/api/v1/runs/"+url.PathEscape(c.run)+"/jobs/"+url.PathEscape(c.job)+suffix, "", struct {
		Step     string `json:"step_id,omitempty"`
		Markdown string `json:"markdown"`
	}{input.Step, input.Markdown}, nil)
}

// Secret reads a declared secret under the live parent award.
func (c *NativeProcessClient) Secret(ctx context.Context, name string) (string, bool, error) {
	var result controllerwire.SecretValue
	query := url.Values{"run": {c.run}, "pipeline": {c.request.Pipeline}}
	err := c.transport.request(ctx, "GET", "/api/v1/secrets/{name}", "/api/v1/secrets/"+url.PathEscape(name)+"?"+query.Encode(), "", nil, &result)
	return result.Value, result.Masked, err
}

// OIDC mints a token under current persisted execution identity.
func (c *NativeProcessClient) OIDC(ctx context.Context, audience string) (string, time.Time, error) {
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := c.transport.request(ctx, "POST", "/api/v1/runs/{run}/oidc-token", "/api/v1/runs/"+url.PathEscape(c.run)+"/oidc-token", "", struct {
		Audience string `json:"audience"`
	}{audience}, &result)
	return result.Token, result.ExpiresAt, err
}

// Output reads a persisted result under the controller's declared read relation.
func (c *NativeProcessClient) Output(ctx context.Context, run, job string) (json.RawMessage, error) {
	var result struct {
		Output json.RawMessage `json:"output"`
	}
	err := c.transport.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/output", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/output", "", nil, &result)
	return result.Output, err
}

// ChildOutput reads a committed result through its creating job's child relation.
func (c *NativeProcessClient) ChildOutput(ctx context.Context, run, job string) (json.RawMessage, error) {
	var result struct {
		Output json.RawMessage `json:"output"`
	}
	err := c.transport.request(ctx, "GET", "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", "/api/v1/runs/"+url.PathEscape(c.run)+"/children/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/output", "", nil, &result)
	return result.Output, err
}

// Latest selects an authorized terminal pipeline result and its requested job.
func (c *NativeProcessClient) Latest(ctx context.Context, pipeline, job string, age time.Duration) (string, json.RawMessage, error) {
	var result struct {
		ID string `json:"id"`
	}
	query := url.Values{"job": {job}, "run": {c.run}, "max_age": {age.String()}}
	err := c.transport.request(ctx, "GET", "/api/v1/pipelines/{name}/latest", "/api/v1/pipelines/"+url.PathEscape(pipeline)+"/latest?"+query.Encode(), "", nil, &result)
	if err != nil {
		return "", nil, err
	}
	output, err := c.Output(ctx, result.ID, job)
	return result.ID, output, err
}

// StartChild submits sealed parent lineage through the shared intake endpoint.
func (c *NativeProcessClient) StartChild(ctx context.Context, pipeline string, args map[string]string, repo, branch string) (string, error) {
	request := c.request
	request.Pipeline, request.RequestedArgs, request.Origin = pipeline, args, runspec.OriginChild
	request.Lineage = runspec.Lineage{ParentRunID: c.run, ParentJobID: c.job}
	if repo != "" && repo != string(request.Git.Repository.Key) {
		return "", localHostClientRefusal()
	}
	if branch != "" {
		request.Git.Ref = branch
		request.Git.SHA = ""
		request.Git.App = runspec.Optional[runspec.AppFacts]{}
	}
	key := rand.Text()
	var result controllerwire.CreateRunResponse
	err := c.transport.request(ctx, "POST", "/api/v1/runs/{run}/children", "/api/v1/runs/"+url.PathEscape(c.run)+"/children", key, controllerwire.CreateRunRequest{Request: request, IdempotencyKey: key}, &result)
	return result.RunID, err
}

// Child reads an authorized child's current status through the parent relation.
func (c *NativeProcessClient) Child(ctx context.Context, run string, wait time.Duration) (controllerwire.RunDetail, error) {
	var result controllerwire.RunDetail
	query := url.Values{"wait": {wait.String()}}
	err := c.transport.request(ctx, "GET", "/api/v1/runs/{run}/children/{child}", "/api/v1/runs/"+url.PathEscape(c.run)+"/children/"+url.PathEscape(run)+"?"+query.Encode(), "", nil, &result)
	return result, err
}
