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
// node changes. It is decided once the node is ready, when the nodes a generator added are known.
func (s *scheduler) rehydrated(ctx context.Context, n nodeproto.PlanNode, byID map[string]nodeproto.PlanNode) (Result, bool) {
	r := s.rehydrate
	strict := r != nil && len(r.Causes) > 0
	if r == nil || s.rerun[n.ID] || (strict && s.reruns(n.ID, byID, map[string]bool{})) {
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
	case !strict && (errors.Is(err, store.ErrNotFound) || (err == nil && prior.Outcome != string(sparkwing.Success))):
		return s.runsAgain(n.ID)
	case errors.Is(err, store.ErrNotFound):
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("source node %s is absent from retried run %s", n.ID, r.SourceRunID)}, true
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("read source node %s: %w", n.ID, err)}, true
	case prior.Outcome == "":
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("source node %s is not terminal", n.ID)}, true
	}
	// safety: a node planned differently in the source, as a generator may regenerate it, ran other work, so
	// its result is not this node's to copy.
	if prior.SpecHash != n.SpecHash {
		return s.runsAgain(n.ID)
	}
	generator := n.Closures != nil && len(n.Closures.Generator) > 0 && prior.Outcome == string(sparkwing.Success)
	if generator && s.regenerate == nil {
		return s.runsAgain(n.ID)
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
	// safety: a generator's nodes are not recorded whole, so they are generated again from the copied output in
	// an eval process, as a memo hit's are.
	if generator {
		res.expansions = s.regenerate(ctx, &NodeRun{Run: s.run, Plan: n, Generator: s.addedBy[n.ID]}, output)
	}
	return res, true
}

func (s *scheduler) runsAgain(id string) (Result, bool) {
	s.rerun[id] = true
	return Result{}, false
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
	waits, _, err := s.generatedWaits(n, waitsOn(n, byID))
	if err != nil {
		s.rerun[id] = true
		return true
	}
	for _, closure := range n.NeedsGenerated {
		for _, source := range s.nodes {
			if source.Closures != nil && slices.Contains(source.Closures.Generator, closure) {
				waits = append(waits, source.ID)
			}
		}
	}
	rerun := slices.Contains(s.rehydrate.Causes, id) ||
		(n.OnFailureOf != "" && s.reruns(n.OnFailureOf, byID, seen)) ||
		slices.ContainsFunc(waits, func(dep string) bool { return s.reruns(dep, byID, seen) })
	s.rerun[id] = rerun
	return rerun
}

func (s *scheduler) sourceOutput(ctx context.Context, prior *store.Node) (json.RawMessage, bool, error) {
	if prior.OutputRef == nil {
		return nil, true, nil
	}
	outputs, ok := s.run.State.(sourceOutputs)
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
