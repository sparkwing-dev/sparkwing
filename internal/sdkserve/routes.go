package sdkserve

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: every reach outside this process goes through the host's routes,
// which add the controller credential; nothing here holds one.
func withNodeRoutes(ctx context.Context, c *nodeclient.Client, planned *Planned, masker *secrets.Masker) context.Context {
	ctx = sparkwing.WithSecretResolver(ctx, sparkwing.SecretResolverFunc(func(ctx context.Context, name string) (string, bool, error) {
		value, masked, err := c.Secret(ctx, name)
		if nodeclient.HasCode(err, nodeclient.CodeSecretNotFound) {
			return "", false, fmt.Errorf("%w%.0w", err, secretsource.ErrMissing)
		}
		if err == nil && masked {
			masker.Register(value)
		}
		return value, masked, err
	}))
	ctx = sparkwingruntime.WithOutputReader(ctx, func(ctx context.Context, node string) ([]byte, error) {
		if data, ok := localOutput(planned, node); ok {
			return data, nil
		}
		data, err := c.NodeOutput(ctx, node)
		return data, absentOn(err, nodeclient.CodeUnknownNode, nodeclient.CodeNotReady, nodeclient.CodeNoOutput)
	})
	ctx = sparkwingruntime.WithPipelineResolver(ctx, sparkwing.PipelineResolverFunc(
		func(ctx context.Context, pipeline, node string, maxAge time.Duration) (*sparkwing.ResolvedPipelineRef, error) {
			runID, data, err := c.PipelineLatest(ctx, pipeline, node, maxAge)
			if err != nil {
				return nil, absentOn(err, nodeclient.CodeNoRun, nodeclient.CodeNoOutput)
			}
			return &sparkwing.ResolvedPipelineRef{RunID: runID, Data: data}, nil
		}))
	ctx = sparkwingruntime.WithPipelineAwaiter(ctx, sparkwing.PipelineAwaiterFunc(
		func(ctx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
			return awaitChild(ctx, c, req)
		}))
	return sparkwingruntime.WithOIDCTokenSource(ctx, func(ctx context.Context, audience string) (string, error) {
		token, _, err := c.OIDCToken(ctx, audience)
		if nodeclient.HasCode(err, nodeclient.CodeOIDCUnavailable) {
			return "", fmt.Errorf("%w%.0w", err, sparkwing.ErrOIDCUnavailable)
		}
		if err == nil {
			masker.Register(token)
		}
		return token, err
	})
}

// safety: only the codes that mean the output is not there mark an absence,
// so Ref.TryGet never takes a refusal or an unreachable host for a miss.
func absentOn(err error, codes ...string) error {
	for _, code := range codes {
		if nodeclient.HasCode(err, code) {
			return fmt.Errorf("%w%.0w", err, sparkwing.ErrRefAbsent)
		}
	}
	return err
}

// safety: the host holds each await at most a minute, so the SDK asks again
// until the child finishes. req.Timeout bounds the whole call, retries
// included, through the context every request runs under.
func awaitChild(parent context.Context, c *nodeclient.Client, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
	ctx := parent
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, req.Timeout)
		defer cancel()
	}
	timedOut := func(what string, err error) error {
		if parent.Err() == nil && ctx.Err() != nil {
			return fmt.Errorf("%s did not finish within %s: %w", what, req.Timeout, context.DeadlineExceeded)
		}
		return err
	}
	run, err := c.StartChild(ctx, nodeclient.ChildRequest{Pipeline: req.Pipeline, Args: req.Args, Repo: req.Repo, Branch: req.Branch})
	if err != nil {
		return nil, timedOut("child run of "+req.Pipeline, fmt.Errorf("start child run: %w", err))
	}
	for {
		wait := nodeclient.MaxAwait
		if deadline, ok := ctx.Deadline(); ok {
			left := time.Until(deadline)
			if left < time.Millisecond {
				<-ctx.Done()
				return nil, timedOut("child run "+run, ctx.Err())
			}
			wait = min(wait, left)
		}
		st, err := c.AwaitChild(ctx, run, req.NodeID, wait)
		switch {
		case err != nil:
			return nil, timedOut("child run "+run, fmt.Errorf("await child run %s: %w", run, err))
		case st.Status == "success":
			return &sparkwing.ResolvedPipelineRef{RunID: run, Data: st.Output}, nil
		case st.Status == "failed":
			return nil, fmt.Errorf("child run %s failed: %s", run, st.Error)
		case st.Status == "cancelled":
			return nil, errors.New("child run " + run + " was cancelled")
		}
	}
}
