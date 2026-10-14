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

func isClaimToken(token string) bool {
	return strings.HasPrefix(token, store.ClaimTokenPrefix+"_")
}

// safety: the claim token reaches only the claim routes, so the node's outcome
// is held here and reported once as the claim's attempt, and a child run is
// started and read through the run's own claim.
type claimState struct {
	*client.Client
	runID, nodeID string
	calls         atomic.Int64

	mu       sync.Mutex
	finished bool
	report   store.AttemptReport
	manifest string
}

// safety: the manifest rides in the attempt's report, the one write a claim
// makes to its node.
func (s *claimState) SetNodeArtifactManifest(_ context.Context, _, _, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest = digest
	return nil
}

func (s *claimState) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return s.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, "", nil)
}

// safety: the output is uploaded while the claim is live and the report
// names only the committed object; an upload that fails records nothing, so
// the node executor's failed finish becomes the attempt's report.
func (s *claimState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, _ *int) error {
	ref, err := s.UploadNodeOutput(ctx, runID, nodeID, output)
	if err != nil {
		return fmt.Errorf("%w: %w", store.ErrOutputNotStored, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	s.report = store.AttemptReport{Outcome: outcome, Error: errMsg, FailureReason: reason, Output: ref}
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

func (s *claimState) attempt(res runner.Result, runErr error) store.AttemptReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		report := s.report
		report.ArtifactManifest = s.manifest
		return report
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
