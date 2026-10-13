package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
	// Causes, when set, names the nodes an automatic retry runs again, with
	// every node that depends on one and the on_failure node of one; every
	// other node copies its source result, whatever it was. Empty copies only
	// the nodes that succeeded in the source run.
	Causes []string
}

type sourceOutputs interface {
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
}

// safety: the dispatcher's rule: a manual retry runs again every node that did not succeed, and an automatic
// retry runs exactly its causes and what they reach, so a result copied from the source is never one a rerun
// node changes.
func (s *scheduler) rehydrated(ctx context.Context, n nodeproto.PlanNode, byID map[string]nodeproto.PlanNode) (Result, bool) {
	r := s.rehydrate
	strict := r != nil && len(r.Causes) > 0
	if r == nil || s.rerun[n.ID] || (strict && s.reruns(n.ID, byID, map[string]bool{})) {
		return Result{}, false
	}
	prior, err := s.run.State.GetNode(ctx, r.SourceRunID, n.ID)
	switch {
	case !strict && (errors.Is(err, store.ErrNotFound) || (err == nil && prior.Outcome != string(sparkwing.Success))):
		s.rerun[n.ID] = true
		return Result{}, false
	case errors.Is(err, store.ErrNotFound):
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("source node %s is absent from retried run %s", n.ID, r.SourceRunID)}, true
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("read source node %s: %w", n.ID, err)}, true
	case prior.Outcome == "":
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("source node %s is not terminal", n.ID)}, true
	}
	output, err := s.copyRetried(ctx, prior)
	if err != nil {
		return Result{Outcome: sparkwing.Failed, Err: err}, true
	}
	res := Result{Outcome: sparkwing.Outcome(prior.Outcome), Reason: prior.FailureReason, Output: output, memoRecorded: true}
	if prior.Error != "" {
		res.Err = errors.New(prior.Error)
	}
	return res, true
}

func (s *scheduler) reruns(id string, byID map[string]nodeproto.PlanNode, seen map[string]bool) bool {
	if rerun, ok := s.rerun[id]; ok {
		return rerun
	}
	if seen[id] {
		return false
	}
	seen[id] = true
	n := byID[id]
	rerun := slices.Contains(s.rehydrate.Causes, id) ||
		(n.OnFailureOf != "" && s.reruns(n.OnFailureOf, byID, seen)) ||
		slices.ContainsFunc(waitsOn(n, byID), func(dep string) bool { return s.reruns(dep, byID, seen) })
	s.rerun[id] = rerun
	return rerun
}

func (s *scheduler) copyRetried(ctx context.Context, prior *store.Node) (json.RawMessage, error) {
	ctx, source, id := context.WithoutCancel(ctx), s.rehydrate.SourceRunID, prior.NodeID
	if prior.ArtifactManifest != "" {
		if err := s.run.State.SetNodeArtifactManifest(ctx, s.run.ID, id, prior.ArtifactManifest); err != nil {
			return nil, fmt.Errorf("copy source node %s artifact: %w", id, err)
		}
	}
	var output []byte
	if prior.OutputRef != nil {
		outputs, ok := s.run.State.(sourceOutputs)
		if !ok {
			return nil, fmt.Errorf("copy source node %s output: the runs store cannot read it", id)
		}
		data, err := outputs.GetNodeOutput(ctx, source, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("read source node %s output: %w", id, err)
		}
		output = data
	}
	if err := s.run.State.FinishNodeWithReason(ctx, s.run.ID, id, prior.Outcome, prior.Error, output, prior.FailureReason, prior.ExitCode); err != nil {
		return nil, fmt.Errorf("copy source node %s outcome: %w", id, err)
	}
	payload, err := json.Marshal(map[string]string{"prior_run_id": source})
	if err != nil {
		return nil, err
	}
	if err := s.run.State.AppendEvent(ctx, s.run.ID, id, "node_skipped_from_retry", payload); err != nil {
		return nil, fmt.Errorf("record source node %s rehydration: %w", id, err)
	}
	return output, nil
}
