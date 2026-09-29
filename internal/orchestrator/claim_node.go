package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// SpecHashEnv hands a controller-dispatched node's pipeline process the spec
// hash its run's accepted plan holds for the node.
const SpecHashEnv = "SPARKWING_NODE_SPEC_HASH"

func isClaimToken(token string) bool {
	return strings.HasPrefix(token, store.ClaimTokenPrefix+"_")
}

// safety: the claim token reaches only the claim routes, so the node's outcome
// is held here and reported once as the claim's attempt, and a child run is
// started and read through the run's own claim.
type claimState struct {
	*client.Client
	runID, nodeID, specHash string
	calls                   atomic.Int64

	mu       sync.Mutex
	finished bool
	report   store.AttemptReport
}

func (s *claimState) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return s.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, "", nil)
}

func (s *claimState) FinishNodeWithReason(_ context.Context, _, _, outcome, errMsg string, output []byte, reason string, _ *int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	s.report = store.AttemptReport{Outcome: outcome, Error: errMsg, FailureReason: reason, Output: output}
	return nil
}

func (s *claimState) AppendEvent(context.Context, string, string, string, []byte) error { return nil }

func (s *claimState) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string,
	_, _, _, _, _, repo, branch string,
) (string, error) {
	return s.EnqueueChildRun(ctx, s.runID, client.ChildRun{
		Ordinal: s.calls.Add(1) - 1, Pipeline: pipeline, Args: args, Repo: repo, Branch: branch,
	})
}

// safety: a child inherits its parent's trigger on the controller, so the
// environment a local await would forward is not sent.
func (s *claimState) EnqueueTriggerWithEnv(ctx context.Context, pipeline string, args map[string]string,
	parentRunID, parentNodeID, retryOf, source, user, repo, branch string, _ map[string]string,
) (string, error) {
	return s.EnqueueTrigger(ctx, pipeline, args, parentRunID, parentNodeID, retryOf, source, user, repo, branch)
}

func (s *claimState) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	if runID == s.runID {
		return s.Client.GetRun(ctx, runID)
	}
	return s.GetChildRun(ctx, s.runID, runID)
}

func (s *claimState) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	if runID == s.runID {
		return s.Client.GetNodeOutput(ctx, runID, nodeID)
	}
	return s.GetChildNodeOutput(ctx, s.runID, runID, nodeID)
}

func (s *claimState) input(ctx context.Context, req store.ClaimInputRequest) ([]byte, error) {
	in, err := s.ClaimInput(ctx, s.runID, s.nodeID, req)
	if err != nil {
		return nil, err
	}
	return in.Output, nil
}

// safety: a claim reads another pipeline's newest successful run only through
// the reference the node's plan declares, and the controller picks the run.
func (s *claimState) pipelineRefResolver() sparkwing.PipelineResolverFunc {
	return func(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*sparkwing.ResolvedPipelineRef, error) {
		in, err := s.ClaimInput(ctx, s.runID, s.nodeID, store.ClaimInputRequest{
			Kind: store.ClaimInputLastRun, Pipeline: pipeline, Node: nodeID, MaxAgeMS: maxAge.Milliseconds(),
		})
		if err != nil {
			return nil, fmt.Errorf("no matching run for pipeline %q (maxAge=%s): %w", pipeline, maxAge, absentIfNotFound(err))
		}
		return &sparkwing.ResolvedPipelineRef{RunID: in.RunID, Data: in.Output}, nil
	}
}

func (s *claimState) attempt(res runner.Result, runErr error) store.AttemptReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return s.report
	}
	report := store.AttemptReport{Outcome: string(sparkwing.Failed), FailureReason: store.FailureUnknown}
	if err := errors.Join(runErr, res.Err); err != nil {
		report.Error = err.Error()
		if errors.Is(err, store.ErrClaimCancelRequested) {
			report.Outcome, report.FailureReason = string(sparkwing.Cancelled), ""
		}
	}
	return report
}

// safety: the launcher Job's trusted process already recorded the execution
// start and renews the claim, so this process only runs the node and reports.
func runClaimedNode(ctx context.Context, controllerURL, logsURL, runID, nodeID, token string) error {
	c := client.NewWithToken(controllerURL, nil, token)
	state := &claimState{Client: c, runID: runID, nodeID: nodeID, specHash: os.Getenv(SpecHashEnv)}
	go renewCacheGrant(ctx, controllerURL, token, runID)
	res, err := RunNodeOnce(ctx, controllerURL, logsURL, runID, nodeID, "claim:"+runID+"/"+nodeID, token,
		selectLocalRenderer(), slog.Default(), nil, func(cfg *runNodeConfig) { cfg.claim = state })
	report := state.attempt(res, err)
	if rerr := c.ReportAttempt(context.WithoutCancel(ctx), runID, nodeID, report); rerr != nil {
		return fmt.Errorf("report the attempt: %w", rerr)
	}
	if report.Outcome == string(sparkwing.Failed) {
		return fmt.Errorf("node %s/%s failed: %s", runID, nodeID, report.Error)
	}
	return nil
}

// perf: a grant lives five minutes; renewing at two keeps one miss from
// letting it lapse under a long step.
const cacheGrantRenewal = 2 * time.Minute

// safety: the grant is minted through the claim, so it stops renewing the
// moment the claim ends or its run is cancelled, and lapses soon after.
func renewCacheGrant(ctx context.Context, controllerURL, token, runID string) {
	t := time.NewTicker(cacheGrantRenewal)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			grant := RequestRunCacheGrant(ctx, controllerURL, token, runID, slog.Default())
			if grant == "" {
				continue
			}
			if err := os.Setenv(authwire.CacheGrantEnv, grant); err != nil {
				slog.Default().Warn("renew the cache grant", "run_id", runID, "err", err)
			}
		}
	}
}
