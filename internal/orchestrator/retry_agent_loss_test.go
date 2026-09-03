package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type rehydrateReadFailureState struct {
	orchestrator.StateBackend
	runID  string
	nodeID string
}

func (s rehydrateReadFailureState) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	if runID == s.runID {
		return nil, errors.New("injected source read failure")
	}
	lister := s.StateBackend.(interface {
		ListNodes(context.Context, string) ([]*store.Node, error)
	})
	return lister.ListNodes(ctx, runID)
}

func (s rehydrateReadFailureState) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	if runID == s.runID && nodeID == s.nodeID {
		return nil, errors.New("injected source read failure")
	}
	return s.StateBackend.GetNode(ctx, runID, nodeID)
}

var agentLossCounts = struct {
	sync.Mutex
	values map[string]int
}{values: map[string]int{}}

type agentLossRetryPipeline struct{ sparkwing.Base }

func (agentLossRetryPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	cause := sparkwing.Job(plan, "cause", func(context.Context) error { countAgentLossNode("cause"); return nil })
	sparkwing.Job(plan, "descendant", func(context.Context) error { countAgentLossNode("descendant"); return nil }).Needs(cause)
	sparkwing.Job(plan, "optional-descendant", func(context.Context) error { countAgentLossNode("optional-descendant"); return nil }).NeedsOptional(cause)
	sparkwing.Job(plan, "unrelated-failure", func(context.Context) error { countAgentLossNode("unrelated-failure"); return nil }).
		OnFailure("unrelated-cleanup", func(context.Context) error { countAgentLossNode("unrelated-cleanup"); return nil })
	sparkwing.Job(plan, "unrelated-success", func(context.Context) error { countAgentLossNode("unrelated-success"); return nil })
	return nil
}

func init() {
	register("agent-loss-selective-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &agentLossRetryPipeline{} })
}

func TestRun_AgentLossRetryRerunsOnlyCausesAndDescendants(t *testing.T) {
	agentLossCounts.Lock()
	agentLossCounts.values = map[string]int{}
	agentLossCounts.Unlock()
	paths := newPaths(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "source", Pipeline: "agent-loss-selective-retry", Status: "failed", StartedAt: time.Now(), PlanSnapshot: agentLossRetrySnapshot()}); err != nil {
		t.Fatal(err)
	}
	for _, node := range []struct {
		id, outcome, reason, artifact string
	}{
		{"cause", "failed", store.FailureAgentLost, ""},
		{"descendant", "cancelled", store.FailureUnknown, ""},
		{"optional-descendant", "success", store.FailureUnknown, ""},
		{"unrelated-failure", "failed", store.FailureVerify, "sha256:failure-artifact"},
		{"unrelated-cleanup", "success", store.FailureUnknown, ""},
		{"unrelated-success", "success", store.FailureUnknown, "sha256:success-artifact"},
	} {
		deps := []string(nil)
		if node.id == "descendant" || node.id == "optional-descendant" {
			deps = []string{"cause"}
		}
		if err := st.CreateNode(ctx, store.Node{RunID: "source", NodeID: node.id, Status: "pending", Deps: deps}); err != nil {
			t.Fatal(err)
		}
		if node.artifact != "" {
			if err := st.SetNodeArtifactManifest(ctx, "source", node.id, node.artifact); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.FinishNodeWithReason(ctx, "source", node.id, node.outcome, "prior "+node.outcome, json.RawMessage(`{"prior":true}`), node.reason, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "retry", Pipeline: "agent-loss-selective-retry", Status: "pending",
		StartedAt: time.Now(), RetryOf: "source", RetrySource: store.RetrySourceAuto,
	}); err != nil {
		t.Fatal(err)
	}
	causes, _ := json.Marshal([]string{"cause"})
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "retry", "source", "source", causes,
		time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano(), 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{
		Pipeline: "agent-loss-selective-retry", RunID: "retry", RetryOf: "source", RetrySource: store.RetrySourceAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed from preserved unrelated failure", result.Status)
	}
	for nodeID, want := range map[string]int{"cause": 1, "descendant": 1, "optional-descendant": 1, "unrelated-failure": 0, "unrelated-cleanup": 0, "unrelated-success": 0} {
		if got := agentLossNodeCount(nodeID); got != want {
			t.Errorf("%s invocations = %d, want %d", nodeID, got, want)
		}
	}
	st, err = store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for nodeID, wantArtifact := range map[string]string{
		"unrelated-failure": "sha256:failure-artifact", "unrelated-success": "sha256:success-artifact",
	} {
		node, err := st.GetNode(ctx, "retry", nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if node.ArtifactManifest != wantArtifact {
			t.Errorf("%s artifact = %q, want %q", nodeID, node.ArtifactManifest, wantArtifact)
		}
	}
}

func TestRun_AgentLossRetryRehydratesCompletedDynamicSibling(t *testing.T) {
	items := []string{"done", "lost"}
	discoverItems.Store(&items)
	builtMu.Lock()
	builtImages = nil
	builtMu.Unlock()
	paths := newPaths(t)
	ctx := context.Background()
	source, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{Pipeline: "expand-ok", RunID: "dynamic-source"})
	if err != nil || source.Status != "success" {
		t.Fatalf("source run = %+v, %v", source, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes
SET status = 'done', outcome = 'failed', error = 'agent lost', failure_reason = ?, attempts_consumed = 0
WHERE run_id = 'dynamic-source' AND node_id = 'build-lost'`, store.FailureAgentLost); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET status = 'failed', finished_at = ? WHERE id = 'dynamic-source'`, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "dynamic-retry", Pipeline: "expand-ok", Status: "pending", StartedAt: time.Now(),
		RetryOf: "dynamic-source", RetrySource: store.RetrySourceAuto,
	}); err != nil {
		t.Fatal(err)
	}
	causes, _ := json.Marshal([]string{"build-lost"})
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "dynamic-retry", "dynamic-source", "dynamic-source", causes,
		time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano(), 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	builtMu.Lock()
	builtImages = nil
	builtMu.Unlock()
	faninRan.Store(false)

	retry, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{
		Pipeline: "expand-ok", RunID: "dynamic-retry", RetryOf: "dynamic-source", RetrySource: store.RetrySourceAuto,
	})
	if err != nil || retry.Status != "success" {
		t.Fatalf("retry run = %+v, %v", retry, err)
	}
	builtMu.Lock()
	built := append([]string{}, builtImages...)
	builtMu.Unlock()
	if len(built) != 1 || built[0] != "lost" {
		t.Fatalf("dynamic child invocations = %v, want only lost", built)
	}
	if !faninRan.Load() {
		t.Fatal("fanin did not run after the lost child was retried")
	}
	st, err = store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	completed, err := st.GetNode(ctx, "dynamic-retry", "build-done")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Outcome != "success" {
		t.Fatalf("rehydrated dynamic sibling outcome = %q", completed.Outcome)
	}
}

func TestRun_AgentLossRetryRejectsChangedDynamicChildSemantics(t *testing.T) {
	items := []string{"lost"}
	discoverItems.Store(&items)
	paths := newPaths(t)
	ctx := context.Background()
	source, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{Pipeline: "expand-ok", RunID: "dynamic-drift-source"})
	if err != nil || source.Status != "success" {
		t.Fatalf("source run = %+v, %v", source, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := st.GetRun(ctx, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(sourceRun.PlanSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, rawNode := range snapshot["nodes"].([]any) {
		node := rawNode.(map[string]any)
		if node["id"] == "build-lost" {
			node["env"] = map[string]string{"DRIFT": "changed"}
		}
	}
	driftedSnapshot, _ := json.Marshal(snapshot)
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET plan_json = ?, status = 'failed', finished_at = ? WHERE id = ?`,
		driftedSnapshot, time.Now().UnixNano(), source.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes
SET status = 'done', outcome = 'failed', error = 'agent lost', failure_reason = ?, attempts_consumed = 0
WHERE run_id = ? AND node_id = 'build-lost'`, store.FailureAgentLost, source.RunID); err != nil {
		t.Fatal(err)
	}
	var definitionHash string
	if err := st.DB().QueryRowContext(ctx, `SELECT plan_hash FROM run_definition_plans WHERE run_id = ?`, source.RunID).Scan(&definitionHash); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "dynamic-drift-retry", Pipeline: "expand-ok", Status: "pending", StartedAt: time.Now(),
		RetryOf: source.RunID, RetrySource: store.RetrySourceAuto,
	}); err != nil {
		t.Fatal(err)
	}
	causes, _ := json.Marshal([]string{"build-lost"})
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "dynamic-drift-retry", source.RunID, source.RunID, causes,
		time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano(), 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	builtMu.Lock()
	builtImages = nil
	builtMu.Unlock()

	retry, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{
		Pipeline: "expand-ok", RunID: "dynamic-drift-retry", RetryOf: source.RunID,
		RetrySource: store.RetrySourceAuto, RetryPlanHash: definitionHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "failed" {
		t.Fatalf("retry status = %q, want failed", retry.Status)
	}
	builtMu.Lock()
	built := append([]string{}, builtImages...)
	builtMu.Unlock()
	if len(built) != 0 {
		t.Fatalf("changed dynamic child executed: %v", built)
	}
}

func TestRun_AgentLossRetryFailsClosedWhenSourceCannotBeRehydrated(t *testing.T) {
	agentLossCounts.Lock()
	agentLossCounts.values = map[string]int{}
	agentLossCounts.Unlock()
	paths := newPaths(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "source-read-failure", Pipeline: "agent-loss-selective-retry", Status: "failed", StartedAt: time.Now(), PlanSnapshot: agentLossRetrySnapshot()}); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"cause", "descendant", "optional-descendant", "unrelated-failure", "unrelated-cleanup", "unrelated-success"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "source-read-failure", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, "source-read-failure", nodeID, "success", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "retry-read-failure", Pipeline: "agent-loss-selective-retry", Status: "pending",
		StartedAt: time.Now(), RetryOf: "source-read-failure", RetrySource: store.RetrySourceAuto,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "retry-read-failure", "source-read-failure", "source-read-failure", []byte(`["cause"]`),
		time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano(), 1); err != nil {
		t.Fatal(err)
	}
	backends := orchestrator.LocalBackends(paths, st, nil)
	backends.State = rehydrateReadFailureState{
		StateBackend: backends.State, runID: "source-read-failure", nodeID: "unrelated-success",
	}
	result, err := orchestrator.Run(ctx, backends, orchestrator.Options{
		Pipeline: "agent-loss-selective-retry", RunID: "retry-read-failure",
		RetryOf: "source-read-failure", RetrySource: store.RetrySourceAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.Error == nil || !strings.Contains(result.Error.Error(), "injected source read failure") {
		t.Fatalf("result = %+v, want fail-closed rehydration error", result)
	}
	for _, nodeID := range []string{"cause", "descendant", "optional-descendant", "unrelated-failure", "unrelated-cleanup", "unrelated-success"} {
		if count := agentLossNodeCount(nodeID); count != 0 {
			t.Fatalf("%s invoked %d times despite failed source rehydration", nodeID, count)
		}
	}
}

func agentLossRetrySnapshot() []byte {
	return []byte(`{"pipeline":"agent-loss-selective-retry","nodes":[{"id":"cause","deps":[]},{"id":"descendant","deps":["cause"]},{"id":"optional-descendant","deps":[]},{"id":"unrelated-failure","deps":[]},{"id":"unrelated-cleanup","deps":[],"on_failure_of":"unrelated-failure"},{"id":"unrelated-success","deps":[]}]}`)
}

func countAgentLossNode(id string) {
	agentLossCounts.Lock()
	agentLossCounts.values[id]++
	agentLossCounts.Unlock()
}

func agentLossNodeCount(id string) int {
	agentLossCounts.Lock()
	defer agentLossCounts.Unlock()
	return agentLossCounts.values[id]
}
