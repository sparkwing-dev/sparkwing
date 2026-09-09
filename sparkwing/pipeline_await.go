package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RunAndAwait triggers a fresh run of pipeline and waits for it to
// reach terminal state, returning the typed output of nodeID from that run.
// The controller dispatches a separate process that fetches the target source.
// Use Job to compose jobs within the calling pipeline's process.
//
// Out is the target node's JSON-decoded output type. In is the target pipeline's
// Inputs type passed through WithFreshInputs. Use NoInputs with WithFreshArgs
// when the target's Inputs type cannot be imported.
//
// The controller rejects a pipeline already present in the parent's ancestor
// chain. Callers select a repository with WithFreshRepo for cross-repository runs.
//
//	build, err := sparkwing.RunAndAwait[BuildOut, BuildInputs](
//	    ctx, "widget-build", "artifact",
//	    sparkwing.WithFreshInputs(BuildInputs{Service: "api"}),
//	    sparkwing.WithFreshTimeout(10*time.Minute),
//	)
func RunAndAwait[Out, In any](ctx context.Context, pipeline, nodeID string, opts ...AwaitOption) (Out, error) {
	var zero Out
	cfg := awaitConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	aw := pipelineAwaiterFromContext(ctx)
	if aw == nil {
		return zero, errors.New("sparkwing: RunAndAwait: no awaiter installed in context (called outside the orchestrator?)")
	}
	resolved, err := aw.Await(ctx, AwaitRequest{
		Pipeline: pipeline,
		NodeID:   nodeID,
		Args:     cfg.args,
		Timeout:  cfg.timeout,
		Repo:     cfg.repo,
		Branch:   cfg.branch,
	})
	if err != nil {
		return zero, fmt.Errorf("RunAndAwait(%s/%s): %w", pipeline, nodeID, err)
	}
	if len(resolved.Data) == 0 || string(resolved.Data) == "null" {
		return zero, nil
	}
	var out Out
	if err := json.Unmarshal(resolved.Data, &out); err != nil {
		return zero, fmt.Errorf("RunAndAwait(%s/%s): unmarshal from run %s: %w", pipeline, nodeID, resolved.RunID, err)
	}
	return out, nil
}

// WithFreshInputs flattens a typed Inputs struct into the underlying
// args map. Preferred over WithFreshArgs when the target pipeline's
// Inputs type is importable. Field-to-flag conversion follows the
// `flag:"name"` tag spec; unsupported field types panic.
func WithFreshInputs[T any](in T) AwaitOption {
	args, err := flattenInputs(in)
	if err != nil {
		panic(fmt.Sprintf("sparkwing.WithFreshInputs: %v", err))
	}
	return func(c *awaitConfig) {
		if c.args == nil {
			c.args = make(map[string]string, len(args))
		}
		for k, v := range args {
			c.args[k] = v
		}
	}
}

// AwaitOption configures the triggered run and the wait.
type AwaitOption func(*awaitConfig)

type awaitConfig struct {
	timeout time.Duration
	args    map[string]string
	repo    string
	branch  string
}

// WithFreshTimeout bounds the total wait, including admission queue
// time. On timeout RunAndAwait returns an error; the spawned run
// continues to completion on the controller's schedule (it's not
// cancelled). The default is unbounded (rely on the caller's ctx
// deadline).
func WithFreshTimeout(d time.Duration) AwaitOption {
	return func(c *awaitConfig) { c.timeout = d }
}

// WithFreshArgs passes args through to the spawned trigger. Args are
// not inherited from the parent run; callers opt in to propagation.
func WithFreshArgs(args map[string]string) AwaitOption {
	return func(c *awaitConfig) {
		c.args = make(map[string]string, len(args))
		for k, v := range args {
			c.args[k] = v
		}
	}
}

// WithFreshRepo selects the repository containing the spawned pipeline.
// Use an owner/repository name such as "example/widgets" for cross-repository
// awaits. The child runs at main's tip unless WithFreshBranch selects a branch.
func WithFreshRepo(repo string) AwaitOption {
	return func(c *awaitConfig) { c.repo = repo }
}

// WithFreshBranch overrides the branch the spawned trigger runs
// against. Default is "main" when WithFreshRepo is set; otherwise
// the spawn inherits the parent's branch.
func WithFreshBranch(branch string) AwaitOption {
	return func(c *awaitConfig) { c.branch = branch }
}

// AwaitRequest is the awaiter's input struct. Implementations POST a
// trigger, poll for terminal state, and fetch the target node's
// output.
type AwaitRequest struct {
	Pipeline string
	NodeID   string
	Args     map[string]string
	Timeout  time.Duration
	// Repo, when non-empty, declares which repo the spawned pipeline
	// lives in. Required for cross-repo awaits; empty falls back to
	// parent-run inheritance.
	Repo string
	// Branch overrides the default branch for the spawned trigger
	// (effective only when Repo is also set). Empty -> "main".
	Branch string
}

// PipelineAwaiter is the orchestrator-installed backend for
// RunAndAwait. Both local mode and cluster-mode pod runners
// provide an implementation; user code never implements this.
type PipelineAwaiter interface {
	Await(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)
}

// PipelineAwaiterFunc adapts a plain function to PipelineAwaiter.
type PipelineAwaiterFunc func(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)

func (f PipelineAwaiterFunc) Await(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error) {
	return f(ctx, req)
}

func pipelineAwaiterFromContext(ctx context.Context) PipelineAwaiter {
	if a, ok := ctx.Value(keyPipelineAwaiter).(PipelineAwaiter); ok {
		return a
	}
	return nil
}
