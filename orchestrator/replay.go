package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// runReplayNodeCLI implements `wing replay-node <runID> <nodeID>`.
// runID must be a replay run minted by MintReplayRun.
func runReplayNodeCLI(args []string) error {
	fs := flag.NewFlagSet("replay-node", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	runID := fs.Arg(0)
	nodeID := fs.Arg(1)
	if runID == "" || nodeID == "" {
		return errors.New("replay-node: <runID> and <nodeID> are required")
	}

	paths, err := DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure root: %w", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()

	res, err := RunReplayNode(context.Background(), paths, st, runID, nodeID, selectLocalRenderer())
	if err != nil {
		return err
	}
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "replay-node %s/%s failed: %v\n", runID, nodeID, res.Err)
		return res.Err
	}
	fmt.Fprintf(os.Stderr, "replay-node %s/%s outcome=%s\n", runID, nodeID, res.Outcome)
	return nil
}

// RunReplayNode executes one node from a replay run. Reconstitutes
// input from the original run's dispatch snapshot. Loud-fails on type
// drift or truncated envelopes.
func RunReplayNode(ctx context.Context, paths Paths, st *store.Store, runID, nodeID string, delegate sparkwing.Logger) (runner.Result, error) {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get replay run %s: %w", runID, err)
	}
	if run.ReplayOfRunID == "" || run.ReplayOfNodeID == "" {
		return runner.Result{}, fmt.Errorf("run %s is not a replay (replay_of_* unset)", runID)
	}
	snap, err := st.GetNodeDispatch(ctx, run.ReplayOfRunID, run.ReplayOfNodeID, -1)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get original dispatch %s/%s: %w",
			run.ReplayOfRunID, run.ReplayOfNodeID, err)
	}
	var env dispatchEnvelope
	if err := json.Unmarshal(snap.InputEnvelope, &env); err != nil {
		return runner.Result{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if env.Version != dispatchEnvelopeVersion {
		return runner.Result{}, fmt.Errorf("envelope version %d unsupported (binary expects %d)",
			env.Version, dispatchEnvelopeVersion)
	}
	if envelopeTruncated(snap.InputEnvelope) {
		return runner.Result{}, errors.New("dispatch envelope is truncated; cannot replay")
	}

	reg, ok := sparkwing.Lookup(run.Pipeline)
	if !ok {
		return runner.Result{}, fmt.Errorf("pipeline %q not registered in this binary", run.Pipeline)
	}
	rc := sparkwing.RunContext{
		RunID:    run.ID,
		Pipeline: run.Pipeline,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, run.Repo, run.RepoURL),
		Trigger:   sparkwing.TriggerInfo{Source: "replay"},
		StartedAt: run.StartedAt,
	}
	plan, err := reg.Invoke(ctx, run.Args, rc)
	if err != nil {
		return runner.Result{}, fmt.Errorf("build plan: %w", err)
	}

	target := plan.Node(nodeID)
	if target == nil {
		for _, exp := range plan.Expansions() {
			children := invokeGeneratorForPod(ctx, exp)
			for _, c := range children {
				if c.ID() == nodeID {
					target = c
					break
				}
			}
			if target != nil {
				break
			}
		}
	}
	if target == nil {
		return runner.Result{}, fmt.Errorf("node %q not found in plan for %s", nodeID, run.Pipeline)
	}

	currentType := fmt.Sprintf("%T", target.Job())
	if env.TypeName != currentType {
		return runner.Result{}, fmt.Errorf(
			"code drift: snapshot type %q != current %q (rebuild and replay, or restore the prior pipeline binary)",
			env.TypeName, currentType)
	}
	if len(env.ScalarFields) > 0 && string(env.ScalarFields) != "null" {
		if err := json.Unmarshal(env.ScalarFields, target.Job()); err != nil {
			return runner.Result{}, fmt.Errorf("unmarshal scalar fields onto %s: %w", currentType, err)
		}
	}

	backends := LocalBackends(paths, st)

	// Replay-run nodes first (chain-of-replays sees latest), then
	// fall back to the original.
	originalRunID := run.ReplayOfRunID
	ctx = sparkwing.WithJSONResolver(ctx, func(id string) ([]byte, bool) {
		if data, err := st.GetNode(ctx, runID, id); err == nil && len(data.Output) > 0 {
			return data.Output, true
		}
		if data, err := st.GetNode(ctx, originalRunID, id); err == nil && len(data.Output) > 0 {
			return data.Output, true
		}
		return nil, false
	})

	masker := secrets.NewMasker()
	for _, v := range reg.SecretValues(run.Args) {
		masker.Register(v)
	}
	src := secrets.NewDotenvSource("")
	ctx = sparkwing.WithSecretResolver(ctx, secrets.NewCached(src, masker).AsResolver())
	ctx = secrets.WithMasker(ctx, masker)

	r := NewInProcessRunner(backends)
	res := r.RunNode(ctx, runner.Request{
		RunID:    runID,
		NodeID:   nodeID,
		Pipeline: run.Pipeline,
		Args:     run.Args,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, run.Repo, run.RepoURL),
		Trigger:  sparkwing.TriggerInfo{Source: "replay"},
		Node:     target,
		Delegate: delegate,
	})

	finalStatus := "success"
	errMsg := ""
	if res.Outcome != sparkwing.Success {
		finalStatus = "failed"
		if res.Err != nil {
			errMsg = res.Err.Error()
		}
	}
	if ferr := st.FinishRun(ctx, runID, finalStatus, errMsg); ferr != nil {
		// Node was finalized in RunNode; this is best-effort.
		fmt.Fprintf(os.Stderr, "warning: finish replay run %s: %v\n", runID, ferr)
	}
	return res, nil
}

// envelopeTruncated reports whether b is the "truncated":true stub.
func envelopeTruncated(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	var probe struct {
		Truncated bool `json:"truncated"`
	}
	_ = json.Unmarshal(b, &probe)
	return probe.Truncated
}

// MintReplayRun creates the run + node rows for a replay. New run
// inherits pipeline/args/git from the original and stamps replay_of_*.
func MintReplayRun(ctx context.Context, st *store.Store, originalRunID, nodeID string) (newRunID string, err error) {
	orig, err := st.GetRun(ctx, originalRunID)
	if err != nil {
		return "", fmt.Errorf("get original run %s: %w", originalRunID, err)
	}
	origNode, err := st.GetNode(ctx, originalRunID, nodeID)
	if err != nil {
		return "", fmt.Errorf("get original node %s/%s: %w", originalRunID, nodeID, err)
	}
	if _, err := st.GetNodeDispatch(ctx, originalRunID, nodeID, -1); err != nil {
		return "", fmt.Errorf("no dispatch snapshot for %s/%s: %w", originalRunID, nodeID, err)
	}
	newRunID = localNewRunID()
	if err := st.CreateRun(ctx, store.Run{
		ID:             newRunID,
		Pipeline:       orig.Pipeline,
		Status:         "running",
		TriggerSource:  "replay",
		GitBranch:      orig.GitBranch,
		GitSHA:         orig.GitSHA,
		Args:           orig.Args,
		Repo:           orig.Repo,
		RepoURL:        orig.RepoURL,
		GithubOwner:    orig.GithubOwner,
		GithubRepo:     orig.GithubRepo,
		StartedAt:      time.Now(),
		ReplayOfRunID:  originalRunID,
		ReplayOfNodeID: nodeID,
	}); err != nil {
		return "", fmt.Errorf("create replay run: %w", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID:       newRunID,
		NodeID:      nodeID,
		Status:      "pending",
		Deps:        origNode.Deps,
		NeedsLabels: origNode.NeedsLabels,
	}); err != nil {
		return "", fmt.Errorf("create replay node: %w", err)
	}
	return newRunID, nil
}
