package trustedphaseb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	corerunner "github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	testPipeline = "phase-b-acceptance"
	testRunID    = "run-authoritative"
	testRepo     = "example/project"
	testSHA      = "0123456789abcdef0123456789abcdef01234567"
	testProfile  = "trusted-phaseb"
	testGroup    = "phase-b-shards"
	testMemory   = int64(640 * 1024 * 1024)
)

var errExecutor = errors.New("executor failed")

type fakeExecutor struct {
	begins   []BeginRequest
	executes []ExecuteRequest
	result   corerunner.Result
}

func (f *fakeExecutor) Begin(_ context.Context, req BeginRequest) (string, error) {
	f.begins = append(f.begins, req)
	return "session-capability", nil
}

func (f *fakeExecutor) Execute(_ context.Context, req ExecuteRequest) corerunner.Result {
	f.executes = append(f.executes, req)
	return f.result
}

func testConfig() Config {
	nodes := make([]NodePolicy, 8)
	for i := range nodes {
		nodes[i] = NodePolicy{ID: fmt.Sprintf("shard-%d", i+1), Steps: []string{"run"}}
	}
	return Config{
		Pipeline:    testPipeline,
		Repo:        testRepo,
		ProfileName: testProfile,
		Group:       testGroup,
		Capacity:    4,
		Scope:       sparkwing.ScopeBox,
		Cores:       0.75,
		MemoryBytes: testMemory,
		Nodes:       nodes,
	}
}

func validProjection(t *testing.T) []byte {
	t.Helper()
	nodes := make([]map[string]any, 8)
	for i := range nodes {
		nodes[i] = map[string]any{
			"id":   fmt.Sprintf("shard-%d", i+1),
			"deps": []string{},
			"modifiers": map[string]any{
				"conc_group":       testGroup,
				"conc_capacity":    4,
				"conc_cost":        1,
				"conc_scope":       "box",
				"conc_on_limit":    "queue",
				"res_cores":        0.75,
				"res_memory_bytes": testMemory,
			},
			"work": map[string]any{
				"steps": []map[string]any{{"id": "run"}},
			},
		}
	}
	raw, err := json.Marshal(map[string]any{
		"pipeline": testPipeline,
		"run_id":   testRunID,
		"nodes":    nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validationRequest(t *testing.T) corerunner.PlanValidationRequest {
	t.Helper()
	projection := validProjection(t)
	return corerunner.PlanValidationRequest{
		RunContext: sparkwing.RunContext{
			RunID:    testRunID,
			Pipeline: testPipeline,
			Git:      sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
			Trigger:  sparkwing.TriggerInfo{Source: "push", User: "operator"},
		},
		ProfileName:      testProfile,
		ProfileIsLocal:   true,
		Projection:       projection,
		ProjectionDigest: digest(projection),
	}
}

func TestRunnerBindsProjectionAndExecutesFixedOrdinal(t *testing.T) {
	executor := &fakeExecutor{result: corerunner.Result{Outcome: sparkwing.Success, Output: "trusted-output"}}
	r := New(testConfig(), executor)
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatalf("ValidatePlan: %v", err)
	}
	if len(executor.begins) != 1 {
		t.Fatalf("begin calls = %d, want 1", len(executor.begins))
	}
	result := r.RunNode(context.Background(), corerunner.Request{
		RunID:          testRunID,
		NodeID:         "shard-3",
		Pipeline:       testPipeline,
		Git:            sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
		Trigger:        sparkwing.TriggerInfo{Source: "push", User: "operator"},
		ProfileName:    testProfile,
		ProfileIsLocal: true,
		PlanDigest:     validation.ProjectionDigest,
		Node: sparkwing.Job(sparkwing.NewPlan(), "shard-3", func(context.Context) error {
			panic("candidate job body executed")
		}),
	})
	if result.Outcome != sparkwing.Success || result.Output != "trusted-output" || result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if len(executor.executes) != 1 || executor.executes[0].Ordinal != 3 || executor.executes[0].Session != "session-capability" {
		t.Fatalf("execute calls = %+v", executor.executes)
	}
}

func TestRunnerRejectsUntrustedProjectionBeforeBegin(t *testing.T) {
	tests := map[string]func(*corerunner.PlanValidationRequest){
		"wrong digest":   func(req *corerunner.PlanValidationRequest) { req.ProjectionDigest = "sha256:bad" },
		"wrong repo":     func(req *corerunner.PlanValidationRequest) { req.RunContext.Git.Repo = "attacker/project" },
		"wrong profile":  func(req *corerunner.PlanValidationRequest) { req.ProfileName = "default" },
		"remote profile": func(req *corerunner.PlanValidationRequest) { req.ProfileIsLocal = false },
		"missing sha":    func(req *corerunner.PlanValidationRequest) { req.RunContext.Git.SHA = "" },
		"wrong trigger":  func(req *corerunner.PlanValidationRequest) { req.RunContext.Trigger.Source = "manual" },
		"mutated shape": func(req *corerunner.PlanValidationRequest) {
			var projection map[string]any
			if err := json.Unmarshal(req.Projection, &projection); err != nil {
				panic(err)
			}
			nodes := projection["nodes"].([]any)
			modifiers := nodes[0].(map[string]any)["modifiers"].(map[string]any)
			modifiers["conc_capacity"] = float64(5)
			req.Projection, _ = json.Marshal(projection)
			req.ProjectionDigest = digest(req.Projection)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			executor := &fakeExecutor{}
			r := New(testConfig(), executor)
			req := validationRequest(t)
			mutate(&req)
			if err := r.ValidatePlan(context.Background(), req); err == nil {
				t.Fatal("ValidatePlan succeeded")
			}
			if len(executor.begins) != 0 {
				t.Fatalf("begin calls = %d, want 0", len(executor.begins))
			}
		})
	}
}

func TestRunnerRejectsNodeOutsideAcceptedSession(t *testing.T) {
	executor := &fakeExecutor{}
	r := New(testConfig(), executor)
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	result := r.RunNode(context.Background(), corerunner.Request{
		RunID:       testRunID,
		NodeID:      "shard-9",
		Pipeline:    testPipeline,
		Git:         sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
		ProfileName: testProfile,
		PlanDigest:  validation.ProjectionDigest,
	})
	if result.Outcome != sparkwing.Failed || result.Err == nil {
		t.Fatalf("result = %+v, want refusal", result)
	}
	if len(executor.executes) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.executes))
	}
}

func TestExecutorFailureIdentityIsPreserved(t *testing.T) {
	executor := &fakeExecutor{result: corerunner.Result{Outcome: sparkwing.Failed, Err: errExecutor}}
	r := New(testConfig(), executor)
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	result := r.RunNode(context.Background(), corerunner.Request{
		RunID: testRunID, NodeID: "shard-1", Pipeline: testPipeline,
		Git:         sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
		ProfileName: testProfile, ProfileIsLocal: true, PlanDigest: validation.ProjectionDigest,
	})
	if result.Outcome != sparkwing.Failed || !errors.Is(result.Err, errExecutor) {
		t.Fatalf("result = %+v, want executor sentinel", result)
	}
}
