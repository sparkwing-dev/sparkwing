package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AwaitPipelineJob triggers a fresh run of pipeline and waits for it
// to reach terminal state, returning the typed output of nodeID from
// that run. Unlike PipelineRef (which reads the most recent successful
// run), AwaitPipelineJob always produces a new run -- use it when
// downstream work needs freshness tied to the current moment.
//
// The two type parameters:
//
//   - Out: the JSON-decoded return type (the target node's output).
//   - In: the target pipeline's Inputs struct, so callers feed args
//     via WithAwaitInputs(in In). Pipelines that take no flags use
//     sparkwing.NoInputs.
//
// Cross-repo is the primary use case: pipeline A in repo foo can
// spawn pipeline B from repo bar without importing bar's Go packages.
// The contract is the wire shape: pipeline name + JSON output schema.
//
// Cycle protection: AwaitPipelineJob carries the current run id as
// parent_run_id on the spawned trigger; the controller walks the
// ancestor chain and rejects the request with 409 if pipeline is
// already in it.
//
//	build, err := sparkwing.AwaitPipelineJob[BuildOut, BuildInputs](
//	    ctx, "my-app-build-main", "artifact",
//	    sparkwing.WithAwaitInputs(BuildInputs{Service: "api"}),
//	    sparkwing.WithAwaitTimeout(10*time.Minute),
//	)
//
// Callers that can't import the target's Inputs type pass
// sparkwing.NoInputs and use WithAwaitArgs as the escape hatch.
func AwaitPipelineJob[Out, In any](ctx context.Context, pipeline, nodeID string, opts ...AwaitOption) (Out, error) {
	var zero Out
	cfg := awaitConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	aw := pipelineAwaiterFromContext(ctx)
	if aw == nil {
		return zero, errors.New("sparkwing: AwaitPipelineJob: no awaiter installed in context (called outside the orchestrator?)")
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
		return zero, fmt.Errorf("AwaitPipelineJob(%s/%s): %w", pipeline, nodeID, err)
	}
	if len(resolved.Data) == 0 || string(resolved.Data) == "null" {
		return zero, nil
	}
	var out Out
	if err := json.Unmarshal(resolved.Data, &out); err != nil {
		return zero, fmt.Errorf("AwaitPipelineJob(%s/%s): unmarshal from run %s: %w", pipeline, nodeID, resolved.RunID, err)
	}
	return out, nil
}

// WithAwaitInputs flattens a typed Inputs struct into the underlying
// args map. Preferred over WithAwaitArgs when the target pipeline's
// Inputs type is importable. Field-to-flag conversion follows the
// `flag:"name"` tag spec; unsupported field types panic.
func WithAwaitInputs[T any](in T) AwaitOption {
	args, err := flattenInputs(in)
	if err != nil {
		panic(fmt.Sprintf("sparkwing.WithAwaitInputs: %v", err))
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

// AwaitOption tunes AwaitPipelineJob's trigger + wait behavior.
type AwaitOption func(*awaitConfig)

type awaitConfig struct {
	timeout time.Duration
	args    map[string]string
	repo    string
	branch  string
}

// WithAwaitTimeout bounds the total wait. On timeout AwaitPipelineJob
// returns an error; the spawned run continues to completion on the
// controller's schedule (it's not cancelled). The default is unbounded
// (rely on the caller's ctx deadline).
func WithAwaitTimeout(d time.Duration) AwaitOption {
	return func(c *awaitConfig) { c.timeout = d }
}

// WithAwaitArgs passes args through to the spawned trigger. Args are
// not inherited from the parent run; callers opt in to propagation.
func WithAwaitArgs(args map[string]string) AwaitOption {
	return func(c *awaitConfig) {
		c.args = make(map[string]string, len(args))
		for k, v := range args {
			c.args[k] = v
		}
	}
}

// WithAwaitRepo declares which repo the spawned pipeline lives in
// (e.g. "owner/repo"). Required for cross-repo awaits: without it
// the controller falls back to inheriting the parent run's repo/SHA,
// which silently builds the wrong code when the awaited pipeline is
// registered in a different repo.
//
// When set, the child trigger lands at the branch tip of `repo`'s
// `main` (no SHA pinning) so the child always builds the latest.
// Pass WithAwaitBranch to override.
func WithAwaitRepo(repo string) AwaitOption {
	return func(c *awaitConfig) { c.repo = repo }
}

// WithAwaitBranch overrides the branch the spawned trigger runs
// against. Default is "main" when WithAwaitRepo is set; otherwise
// the spawn inherits the parent's branch.
func WithAwaitBranch(branch string) AwaitOption {
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
// AwaitPipelineJob. Both local mode and cluster-mode pod runners
// provide an implementation; user code never implements this.
type PipelineAwaiter interface {
	Await(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)
}

// PipelineAwaiterFunc adapts a plain function to PipelineAwaiter.
type PipelineAwaiterFunc func(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)

func (f PipelineAwaiterFunc) Await(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error) {
	return f(ctx, req)
}

// WithPipelineAwaiter installs a PipelineAwaiter into ctx. Intended
// for orchestrator implementations.
func WithPipelineAwaiter(ctx context.Context, a PipelineAwaiter) context.Context {
	return context.WithValue(ctx, keyPipelineAwaiter, a)
}

func pipelineAwaiterFromContext(ctx context.Context) PipelineAwaiter {
	if a, ok := ctx.Value(keyPipelineAwaiter).(PipelineAwaiter); ok {
		return a
	}
	return nil
}
