package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Rehydration makes a run a retry of an earlier one: a node whose result the
// source run already holds is copied instead of run, with its output, its
// artifact manifest and a node_skipped_from_retry event.
type Rehydration struct {
	// SourceRunID is the run retried.
	SourceRunID string
}

type sourceOutputs interface {
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
}

// safety: only successful results can be reused by a new retry run.
func (s *scheduler) rehydrated(ctx context.Context, n nodeproto.PlanNode) (Result, bool) {
	r := s.rehydrate
	if r == nil || s.rerun[n.ID] {
		return Result{}, false
	}
	// safety: a resumed run's row an earlier host of this run started, or another holder still claims, is that
	// holder's; it is never overwritten with a copy, and dispatch waits on it as for any resumed row.
	if s.resume {
		own, err := s.run.State.GetNode(ctx, s.run.ID, n.ID)
		if err == nil && (own.Status != "pending" || own.ClaimedBy != "") {
			return s.runsAgain(n.ID)
		}
	}
	prior, err := s.run.State.GetNode(ctx, r.SourceRunID, n.ID)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && prior.Outcome != string(sparkwing.Success)):
		return s.runsAgain(n.ID)
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("read source node %s: %w", n.ID, err)}, true
	}
	// safety: a node planned differently in the source, as a generator may regenerate it, ran other work, so
	// its result is not this node's to copy.
	if prior.SpecHash != n.SpecHash {
		return s.runsAgain(n.ID)
	}
	generator := generatesNodes(n, sparkwing.Outcome(prior.Outcome))
	var batches []recordedBatch
	if generator {
		var err error
		if batches, err = storedExpansions(ctx, s.run.State, r.SourceRunID, n); err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}, true
		}
		if !allRecorded(batches) && s.regenerate == nil {
			return s.runsAgain(n.ID)
		}
	}
	output, ok, err := s.sourceOutput(ctx, prior)
	switch {
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: err}, true
	case !ok:
		// safety: a result whose recorded output is gone would hand its dependents nothing, so the node runs again.
		return s.runsAgain(n.ID)
	}
	if err := s.copyRetried(ctx, prior, output); err != nil {
		return Result{Outcome: sparkwing.Failed, Err: err}, true
	}
	res := Result{Outcome: sparkwing.Outcome(prior.Outcome), Reason: prior.FailureReason, Output: output, recorded: true}
	if prior.Error != "" {
		res.Err = errors.New(prior.Error)
	}
	// safety: the copy adds the generators' nodes the source run recorded, as they were.
	if generator {
		res.expansions = s.recordedExpansions(ctx, n, batches, func() (json.RawMessage, error) { return output, nil })
	}
	return res, true
}

func (s *scheduler) runsAgain(id string) (Result, bool) {
	s.rerun[id] = true
	return Result{}, false
}

func (s *scheduler) sourceOutput(ctx context.Context, prior *store.Node) (json.RawMessage, bool, error) {
	if prior.OutputRef == nil {
		return nil, true, nil
	}
	outputs, ok := baseState(s.run.State).(sourceOutputs)
	if !ok {
		return nil, false, fmt.Errorf("copy source node %s output: the runs store cannot read it", prior.NodeID)
	}
	data, err := outputs.GetNodeOutput(context.WithoutCancel(ctx), s.rehydrate.SourceRunID, prior.NodeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("read source node %s output: %w", prior.NodeID, err)
	}
	return data, true, nil
}

func (s *scheduler) copyRetried(ctx context.Context, prior *store.Node, output json.RawMessage) error {
	ctx, source, id := context.WithoutCancel(ctx), s.rehydrate.SourceRunID, prior.NodeID
	if prior.ArtifactManifest != "" {
		if err := s.run.State.SetNodeArtifactManifest(ctx, s.run.ID, id, prior.ArtifactManifest); err != nil {
			return fmt.Errorf("copy source node %s artifact: %w", id, err)
		}
	}
	if err := s.run.State.FinishNodeWithReason(ctx, s.run.ID, id, prior.Outcome, prior.Error, output, prior.FailureReason, prior.ExitCode); err != nil {
		return fmt.Errorf("copy source node %s outcome: %w", id, err)
	}
	payload, err := json.Marshal(map[string]string{"prior_run_id": source})
	if err != nil {
		return err
	}
	if err := s.run.State.AppendEvent(ctx, s.run.ID, id, "node_skipped_from_retry", payload); err != nil {
		return fmt.Errorf("record source node %s rehydration: %w", id, err)
	}
	return nil
}
