package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"

	mathrand "math/rand/v2"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

// NativeProcessClient uses a retained controller award for one process's service operations.
type NativeProcessClient struct {
	transport *engineClient
	run, job  string
	request   runspec.Request
	secrets   []string
}

// SecretNames returns the secret names the run's sealed spec resolved, read with the process binding.
func (c *NativeProcessClient) SecretNames() []string {
	return append([]string{}, c.secrets...)
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
	client := &NativeProcessClient{transport: transport, run: c.run, job: job, request: snapshot.Run.Request.Request}
	if snapshot.Run.Spec != nil {
		client.secrets = snapshot.Run.Spec.Resolved.SecretNames
	}
	return client, nil
}

// Annotation commits a masked annotation through the common reporting transaction.
func (c *NativeProcessClient) Annotation(ctx context.Context, input nodewire.AnnotationRequest) error {
	return c.transport.AppendAnnotation(ctx, c.run, c.job, controllerwire.AnnotationWrite{
		Annotation: input.Annotation,
		StepID:     input.Step,
	})
}

// Summary commits a masked summary through the common reporting transaction.
func (c *NativeProcessClient) Summary(ctx context.Context, input nodewire.SummaryRequest) error {
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
	query := url.Values{"run": {c.run}}
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
	var result controllerwire.JobOutput
	err := c.transport.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/output", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/output", "", nil, &result)
	return result.Output, err
}

// ChildOutput reads a committed result through its creating job's child relation.
func (c *NativeProcessClient) ChildOutput(ctx context.Context, run, job string) (json.RawMessage, error) {
	var result controllerwire.JobOutput
	err := retryHeldRead(ctx, func() error {
		return c.transport.request(ctx, "GET", "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", "/api/v1/runs/"+url.PathEscape(c.run)+"/children/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/output", "", nil, &result)
	})
	return result.Output, err
}

// Latest reads successful verified default-branch output unless fromAnyRun is
// true; fromProvenRun reads a proven run at this run's own commit instead.
func (c *NativeProcessClient) Latest(ctx context.Context, pipeline, job string, age time.Duration, fromAnyRun, fromProvenRun bool) (string, json.RawMessage, error) {
	var result controllerwire.LatestRun
	query := url.Values{"job": {job}, "run": {c.run}, "max_age": {age.String()}, "from_any_run": {strconv.FormatBool(fromAnyRun)}}
	if fromProvenRun {
		query.Set("from_proven_run", "true")
	}
	err := c.transport.request(ctx, "GET", "/api/v1/pipelines/{name}/latest", "/api/v1/pipelines/"+url.PathEscape(pipeline)+"/latest?"+query.Encode(), "", nil, &result)
	if err != nil {
		return "", nil, err
	}
	// safety: a controller that predates proven references ignores the filter and
	// answers with its latest default-branch run, so its answer is refused.
	if fromProvenRun && !result.ProvenAtCommit {
		return "", nil, fmt.Errorf("the controller does not support proven-at-commit references; upgrade it before reading %s/%s", pipeline, job)
	}
	output, err := c.Output(ctx, result.ID, job)
	return result.ID, output, err
}

// StartChild submits sealed parent lineage through the shared intake endpoint.
func (c *NativeProcessClient) StartChild(ctx context.Context, pipeline string, args map[string]string, repo, branch string, detached bool) (string, error) {
	parent := c.request
	request := runspec.Request{
		Version: parent.Version, Pipeline: pipeline, RequestedArgs: args, Origin: runspec.OriginChild,
		Mode: runspec.IntakeExecute, Dispatch: parent.Dispatch, Git: parent.Git,
		Source: runspec.Source{
			Location: parent.Source.Location, Ref: parent.Source.Ref, RequestedRef: parent.Source.RequestedRef,
			BundleDigest: parent.Source.BundleDigest, WorkingTree: parent.Source.WorkingTree, CommittedCheckout: parent.Source.CommittedCheckout,
			NoBincache: parent.Source.NoBincache, HashAllFiles: parent.Source.HashAllFiles,
		},
		EnvironmentSource: parent.EnvironmentSource, Env: parent.Env, Options: settings.InheritedOptions(parent.Options), PullRequest: parent.PullRequest,
		ExecutionLocation: parent.ExecutionLocation,
		Lineage:           runspec.Lineage{ParentRunID: c.run, ParentJobID: c.job, RepoInherited: repo == "", Detached: detached},
	}
	if repo != "" && repo != string(request.Git.Repository.Key) {
		remote := repo
		if strings.HasPrefix(remote, "git:") && !strings.Contains(remote, "://") {
			remote = "https://" + strings.TrimPrefix(remote, "git:")
		} else if !strings.Contains(remote, ":") {
			remote = "https://github.com/" + remote
		}
		request.Git = runspec.Git{}
		request.Source = runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}}
		request.PullRequest = runspec.Optional[runspec.PullRequest]{}
		if branch == "" {
			branch = "main"
		}
	}
	if branch != "" {
		request.Git.Ref = branch
		request.Git.SHA = ""
		request.Source.Ref, request.Source.RequestedRef = "", ""
		request.Git.App = runspec.Optional[runspec.AppFacts]{}
		request.Source.WorkingTree, request.Source.CommittedCheckout = false, true
		request.Source.BundleDigest = runspec.Optional[string]{}
	}
	key := rand.Text()
	var result controllerwire.CreateRunResponse
	err := c.transport.request(ctx, "POST", "/api/v1/runs/{run}/children", "/api/v1/runs/"+url.PathEscape(c.run)+"/children", key, controllerwire.CreateRunRequest{Request: request, IdempotencyKey: key}, &result)
	return result.RunID, err
}

// Child observes an authorized child until it finishes or the requested wait elapses.
func (c *NativeProcessClient) Child(ctx context.Context, run string, wait time.Duration) (controllerwire.RunDetail, error) {
	var result controllerwire.RunDetail
	query := url.Values{"wait": {wait.String()}}
	until := time.Now().Add(wait)
	delay := 500 * time.Millisecond
	for {
		err := retryHeldRead(ctx, func() error {
			return c.transport.request(ctx, "GET", "/api/v1/runs/{run}/children/{child}", "/api/v1/runs/"+url.PathEscape(c.run)+"/children/"+url.PathEscape(run)+"?"+query.Encode(), "", nil, &result)
		})
		if err != nil {
			return result, err
		}
		switch result.Status {
		case "pending", "running", "queued":
		default:
			return result, nil
		}
		remaining := time.Until(until)
		if remaining <= 0 {
			return result, nil
		}
		// perf: keep one host await authorized while polling immediate controller snapshots.
		pause := delay/2 + mathrand.N(delay/2)
		if err := waitFor(ctx, min(pause, remaining)); err != nil {
			return result, err
		}
		delay = min(2*delay, 5*time.Second)
	}
}
