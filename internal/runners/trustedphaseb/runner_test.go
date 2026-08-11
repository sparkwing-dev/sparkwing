package trustedphaseb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

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
	begins       []BeginRequest
	executes     []ExecuteRequest
	finalizes    []FinalizeRequest
	beginErrs    []error
	finalizeErrs []error
	finalized    map[string]FinalizeRequest
	result       corerunner.Result
	recovers     int
	recoverErr   error
	generation   string
}

func (f *fakeExecutor) Recover(context.Context) (string, error) {
	f.recovers++
	if f.recoverErr != nil {
		return "", f.recoverErr
	}
	f.generation = fmt.Sprintf("generation-%d", f.recovers)
	return f.generation, nil
}

func (f *fakeExecutor) Begin(_ context.Context, req BeginRequest) (string, error) {
	f.begins = append(f.begins, req)
	if req.Generation != f.generation {
		return "", fmt.Errorf("stale runner generation")
	}
	if _, finalized := f.finalized[req.RunID]; finalized {
		return "", fmt.Errorf("durable run tombstone prevents reopen")
	}
	if len(f.beginErrs) != 0 {
		err := f.beginErrs[0]
		f.beginErrs = f.beginErrs[1:]
		if err != nil {
			return "", err
		}
	}
	return "session-capability", nil
}

func (f *fakeExecutor) Execute(_ context.Context, req ExecuteRequest) corerunner.Result {
	if req.Generation != f.generation {
		return corerunner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("stale runner generation")}
	}
	if _, finalized := f.finalized[req.RunID]; finalized {
		return corerunner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("durable run tombstone prevents execution")}
	}
	f.executes = append(f.executes, req)
	return f.result
}

func (f *fakeExecutor) Finalize(_ context.Context, req FinalizeRequest) error {
	f.finalizes = append(f.finalizes, req)
	if req.Generation != f.generation {
		return corerunner.TerminalFinalizationError(fmt.Errorf("stale runner generation"))
	}
	if prior, ok := f.finalized[req.RunID]; ok {
		if prior.Outcome != req.Outcome || prior.Error != req.Error {
			return corerunner.TerminalFinalizationError(fmt.Errorf("conflicting durable finalization"))
		}
		return nil
	}
	if f.finalized == nil {
		f.finalized = make(map[string]FinalizeRequest)
	}
	f.finalized[req.RunID] = req
	if len(f.finalizeErrs) != 0 {
		err := f.finalizeErrs[0]
		f.finalizeErrs = f.finalizeErrs[1:]
		return err
	}
	return nil
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

func mutateProjection(t *testing.T, req *corerunner.PlanValidationRequest, mutate func(map[string]any, []any, map[string]any, map[string]any)) {
	t.Helper()
	var projection map[string]any
	if err := json.Unmarshal(req.Projection, &projection); err != nil {
		t.Fatal(err)
	}
	nodes := projection["nodes"].([]any)
	node := nodes[0].(map[string]any)
	mutate(projection, nodes, node["modifiers"].(map[string]any), node["work"].(map[string]any))
	req.Projection, _ = json.Marshal(projection)
	req.ProjectionDigest = digest(req.Projection)
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

func nodeRequest(validation corerunner.PlanValidationRequest, nodeID string) corerunner.Request {
	return corerunner.Request{
		RunID: testRunID, NodeID: nodeID, Pipeline: testPipeline,
		Git:         sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
		Trigger:     sparkwing.TriggerInfo{Source: "push", User: "operator"},
		ProfileName: testProfile, ProfileIsLocal: true, PlanDigest: validation.ProjectionDigest,
	}
}

func TestRunnerBindsProjectionAndExecutesFixedOrdinal(t *testing.T) {
	executor := &fakeExecutor{result: corerunner.Result{Outcome: sparkwing.Success, Output: "trusted-output"}}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatalf("ValidatePlan: %v", err)
	}
	if len(executor.begins) != 0 {
		t.Fatalf("begin calls during validation = %d, want 0", len(executor.begins))
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
	if err := r.FinalizeRun(context.Background(), corerunner.RunFinalizationRequest{
		RunID: testRunID, Outcome: sparkwing.Success,
	}); err != nil {
		t.Fatalf("FinalizeRun: %v", err)
	}
	if len(executor.finalizes) != 1 || executor.finalizes[0].Session != "session-capability" {
		t.Fatalf("finalize calls = %+v", executor.finalizes)
	}
	if result := r.RunNode(context.Background(), corerunner.Request{RunID: testRunID}); result.Err == nil {
		t.Fatal("finalized run remained dispatchable")
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
		"trailing json": func(req *corerunner.PlanValidationRequest) {
			req.Projection = append(req.Projection, []byte(` {"second":true}`)...)
			req.ProjectionDigest = digest(req.Projection)
		},
		"mutated shape": func(req *corerunner.PlanValidationRequest) {
			mutateProjection(t, req, func(_ map[string]any, _ []any, _ map[string]any, work map[string]any) {
				work["spawns"] = []any{map[string]any{"id": "candidate"}}
			})
		},
	}
	modifierMutations := map[string]func(map[string]any){
		"retry":             func(m map[string]any) { m["retry"] = 1 },
		"retry backoff":     func(m map[string]any) { m["retry_backoff_ms"] = 1 },
		"retry auto":        func(m map[string]any) { m["retry_auto"] = true },
		"timeout":           func(m map[string]any) { m["timeout_ms"] = 1 },
		"runs on":           func(m map[string]any) { m["runs_on"] = []string{"candidate"} },
		"prefers":           func(m map[string]any) { m["prefers"] = []string{"candidate"} },
		"when runner":       func(m map[string]any) { m["when_runner"] = []string{"candidate"} },
		"cache":             func(m map[string]any) { m["cache"] = true },
		"cache ttl":         func(m map[string]any) { m["cache_ttl_ms"] = 1 },
		"inline":            func(m map[string]any) { m["inline"] = true },
		"optional":          func(m map[string]any) { m["optional"] = true },
		"on failure":        func(m map[string]any) { m["on_failure"] = "candidate" },
		"before hook":       func(m map[string]any) { m["has_before_run"] = true },
		"after hook":        func(m map[string]any) { m["has_after_run"] = true },
		"skip hook":         func(m map[string]any) { m["has_skip_if"] = true },
		"group":             func(m map[string]any) { m["conc_group"] = "other" },
		"cost":              func(m map[string]any) { m["conc_cost"] = 2 },
		"scope":             func(m map[string]any) { m["conc_scope"] = "run" },
		"limit":             func(m map[string]any) { m["conc_on_limit"] = "cancel-others" },
		"queue timeout":     func(m map[string]any) { m["conc_queue_timeout_ms"] = 1 },
		"cancel timeout":    func(m map[string]any) { m["conc_cancel_timeout_ms"] = 1 },
		"resource cores":    func(m map[string]any) { m["res_cores"] = 1 },
		"resource memory":   func(m map[string]any) { m["res_memory_bytes"] = float64(testMemory + 1) },
		"continue on error": func(m map[string]any) { m["continue_on_error"] = true },
	}
	for name, mutate := range modifierMutations {
		mutate := mutate
		tests[name] = func(req *corerunner.PlanValidationRequest) {
			mutateProjection(t, req, func(_ map[string]any, _ []any, modifiers, _ map[string]any) { mutate(modifiers) })
		}
	}
	tests["deps"] = func(req *corerunner.PlanValidationRequest) {
		mutateProjection(t, req, func(_ map[string]any, nodes []any, _ map[string]any, _ map[string]any) {
			nodes[0].(map[string]any)["deps"] = []string{"shard-8"}
		})
	}
	tests["environment"] = func(req *corerunner.PlanValidationRequest) {
		mutateProjection(t, req, func(_ map[string]any, nodes []any, _ map[string]any, _ map[string]any) {
			nodes[0].(map[string]any)["env"] = map[string]string{"ATTACK": "1"}
		})
	}
	tests["dynamic"] = func(req *corerunner.PlanValidationRequest) {
		mutateProjection(t, req, func(_ map[string]any, nodes []any, _ map[string]any, _ map[string]any) {
			nodes[0].(map[string]any)["dynamic"] = true
		})
	}
	projectionMutations := map[string]func(map[string]any){
		"priority":                func(p map[string]any) { p["priority"] = 1 },
		"plan concurrency":        func(p map[string]any) { p["plan_concurrency"] = map[string]any{"capacity": 1} },
		"plan concurrency groups": func(p map[string]any) { p["plan_concurrency_groups"] = []any{map[string]any{"name": "candidate"}} },
		"plan resources":          func(p map[string]any) { p["plan_resources"] = map[string]any{"cores": 1} },
		"secrets":                 func(p map[string]any) { p["secrets"] = map[string]any{"TOKEN": "candidate"} },
	}
	for name, mutate := range projectionMutations {
		mutate := mutate
		tests[name] = func(req *corerunner.PlanValidationRequest) {
			mutateProjection(t, req, func(projection map[string]any, _ []any, _ map[string]any, _ map[string]any) { mutate(projection) })
		}
	}
	nodeMutations := map[string]func(map[string]any){
		"node groups":   func(n map[string]any) { n["groups"] = []string{"candidate"} },
		"approval":      func(n map[string]any) { n["approval"] = map[string]any{"required": true} },
		"on failure of": func(n map[string]any) { n["on_failure_of"] = "shard-8" },
	}
	for name, mutate := range nodeMutations {
		mutate := mutate
		tests[name] = func(req *corerunner.PlanValidationRequest) {
			mutateProjection(t, req, func(_ map[string]any, nodes []any, _ map[string]any, _ map[string]any) {
				mutate(nodes[0].(map[string]any))
			})
		}
	}
	tests["step id"] = func(req *corerunner.PlanValidationRequest) {
		mutateProjection(t, req, func(_ map[string]any, _ []any, _ map[string]any, work map[string]any) {
			work["steps"].([]any)[0].(map[string]any)["id"] = "candidate"
		})
	}
	tests["missing node"] = func(req *corerunner.PlanValidationRequest) {
		mutateProjection(t, req, func(projection map[string]any, nodes []any, _ map[string]any, _ map[string]any) {
			projection["nodes"] = nodes[:7]
		})
	}
	workMutations := map[string]func(map[string]any){
		"spawn each":     func(w map[string]any) { w["spawn_each"] = []any{map[string]any{"id": "candidate"}} },
		"step groups":    func(w map[string]any) { w["step_groups"] = []any{map[string]any{"id": "candidate"}} },
		"result step":    func(w map[string]any) { w["result_step"] = "run" },
		"failure policy": func(w map[string]any) { w["failure_policy"] = "continue" },
	}
	for name, mutate := range workMutations {
		mutate := mutate
		tests[name] = func(req *corerunner.PlanValidationRequest) {
			mutateProjection(t, req, func(_ map[string]any, _ []any, _ map[string]any, work map[string]any) { mutate(work) })
		}
	}
	stepMutations := map[string]func(map[string]any){
		"step needs":   func(s map[string]any) { s["needs"] = []string{"candidate"} },
		"step result":  func(s map[string]any) { s["is_result"] = true },
		"step skip":    func(s map[string]any) { s["has_skip_if"] = true },
		"step finally": func(s map[string]any) { s["finally"] = true },
		"step risks":   func(s map[string]any) { s["risks"] = []string{"candidate"} },
	}
	for name, mutate := range stepMutations {
		mutate := mutate
		tests[name] = func(req *corerunner.PlanValidationRequest) {
			mutateProjection(t, req, func(_ map[string]any, _ []any, _ map[string]any, work map[string]any) {
				mutate(work["steps"].([]any)[0].(map[string]any))
			})
		}
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			executor := &fakeExecutor{}
			r, err := New(context.Background(), testConfig(), executor)
			if err != nil {
				t.Fatal(err)
			}
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
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
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
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	result := r.RunNode(context.Background(), corerunner.Request{
		RunID: testRunID, NodeID: "shard-1", Pipeline: testPipeline,
		Git:         sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
		Trigger:     sparkwing.TriggerInfo{Source: "push", User: "operator"},
		ProfileName: testProfile, ProfileIsLocal: true, PlanDigest: validation.ProjectionDigest,
	})
	if result.Outcome != sparkwing.Failed || !errors.Is(result.Err, errExecutor) {
		t.Fatalf("result = %+v, want executor sentinel", result)
	}
}

func TestRunnerBindsProjectionRunID(t *testing.T) {
	executor := &fakeExecutor{}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	req := validationRequest(t)
	var projection map[string]any
	if err := json.Unmarshal(req.Projection, &projection); err != nil {
		t.Fatal(err)
	}
	projection["run_id"] = "run-replayed"
	req.Projection, _ = json.Marshal(projection)
	req.ProjectionDigest = digest(req.Projection)
	if err := r.ValidatePlan(context.Background(), req); err == nil {
		t.Fatal("ValidatePlan accepted a projection for another run")
	}
	if len(executor.begins) != 0 {
		t.Fatalf("begin calls = %d, want 0", len(executor.begins))
	}
}

func TestRunnerRejectsDuplicateConfiguredNode(t *testing.T) {
	config := testConfig()
	config.Nodes[7].ID = config.Nodes[0].ID
	executor := &fakeExecutor{}
	if _, err := New(context.Background(), config, executor); err == nil {
		t.Fatal("New accepted duplicate configured node ids")
	}
	if executor.recovers != 0 || len(executor.begins) != 0 {
		t.Fatalf("recovery calls = %d, begin calls = %d", executor.recovers, len(executor.begins))
	}
}

func TestRunnerRejectsWrongAdmissionEnvelope(t *testing.T) {
	tests := map[string]func(*Config){
		"node count": func(config *Config) { config.Nodes = config.Nodes[:7] },
		"capacity":   func(config *Config) { config.Capacity = 5 },
		"scope":      func(config *Config) { config.Scope = sparkwing.ScopeRun },
		"cores":      func(config *Config) { config.Cores = 1 },
		"memory":     func(config *Config) { config.MemoryBytes++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			mutate(&config)
			executor := &fakeExecutor{}
			if _, err := New(context.Background(), config, executor); err == nil {
				t.Fatal("New accepted wrong admission envelope")
			}
			if executor.recovers != 0 || len(executor.begins) != 0 {
				t.Fatalf("recovery calls = %d, begin calls = %d", executor.recovers, len(executor.begins))
			}
		})
	}
}

type blockingExecutor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	begins  int
}

func (*blockingExecutor) Recover(context.Context) (string, error) { return "generation-1", nil }

func (b *blockingExecutor) Begin(context.Context, BeginRequest) (string, error) {
	b.mu.Lock()
	b.begins++
	b.mu.Unlock()
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return "session-capability", nil
}

func (*blockingExecutor) Execute(context.Context, ExecuteRequest) corerunner.Result {
	return corerunner.Result{}
}

func (*blockingExecutor) Finalize(context.Context, FinalizeRequest) error { return nil }

func TestRunnerIssuesOneSessionForConcurrentNodeDispatch(t *testing.T) {
	executor := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	request := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	nodeRequest := corerunner.Request{
		RunID: testRunID, NodeID: "shard-1", Pipeline: testPipeline,
		Git:         sparkwing.NewGit("", testSHA, "main", "main", testRepo, "https://example.invalid/project.git"),
		Trigger:     sparkwing.TriggerInfo{Source: "push", User: "operator"},
		ProfileName: testProfile, ProfileIsLocal: true, PlanDigest: request.ProjectionDigest,
	}
	firstDone := make(chan corerunner.Result, 1)
	go func() {
		firstDone <- r.RunNode(context.Background(), nodeRequest)
	}()
	<-executor.entered
	secondDone := make(chan corerunner.Result, 1)
	go func() { secondDone <- r.RunNode(context.Background(), nodeRequest) }()
	close(executor.release)
	<-firstDone
	<-secondDone
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.begins != 1 {
		t.Fatalf("begin calls = %d, want 1", executor.begins)
	}
}

func TestRunnerRetriesUncertainBeginAndFinalizesByRunID(t *testing.T) {
	errResponseLost := errors.New("begin response lost")
	executor := &fakeExecutor{beginErrs: []error{errResponseLost, nil}}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	if result := r.RunNode(context.Background(), nodeRequest(validation, "shard-1")); !errors.Is(result.Err, errResponseLost) {
		t.Fatalf("first result = %+v, want response-loss error", result)
	}
	if result := r.RunNode(context.Background(), nodeRequest(validation, "shard-1")); result.Err != nil {
		t.Fatalf("retry result = %+v, want durable Begin CAS recovery", result)
	}
	if len(executor.begins) != 2 {
		t.Fatalf("begin calls = %d, want 2", len(executor.begins))
	}
	if err := r.FinalizeRun(context.Background(), corerunner.RunFinalizationRequest{
		RunID: testRunID, Outcome: sparkwing.Success,
	}); err != nil {
		t.Fatal(err)
	}
	if len(executor.finalizes) != 1 || executor.finalizes[0].RunID != testRunID || executor.finalizes[0].Session != "session-capability" {
		t.Fatalf("finalizes = %+v", executor.finalizes)
	}
}

func TestRunnerFinalizationRetriesAndRejectsConflict(t *testing.T) {
	errResponseLost := errors.New("finalize response lost")
	executor := &fakeExecutor{finalizeErrs: []error{errResponseLost, nil}}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	if result := r.RunNode(context.Background(), nodeRequest(validation, "shard-1")); result.Err != nil {
		t.Fatalf("RunNode: %+v", result)
	}
	req := corerunner.RunFinalizationRequest{RunID: testRunID, Outcome: sparkwing.Success}
	if err := r.FinalizeRun(context.Background(), req); !errors.Is(err, errResponseLost) {
		t.Fatalf("first finalize error = %v", err)
	}
	if err := r.FinalizeRun(context.Background(), req); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	r.mu.Lock()
	retained := len(r.accepted)
	r.mu.Unlock()
	if retained != 0 {
		t.Fatalf("accepted runs retained after durable finalize = %d", retained)
	}
	if err := r.FinalizeRun(context.Background(), req); err != nil {
		t.Fatalf("acknowledged replay: %v", err)
	}
	if len(executor.finalizes) != 3 {
		t.Fatalf("finalize calls = %d, want 3 including acknowledged durable replay", len(executor.finalizes))
	}
	conflict := corerunner.RunFinalizationRequest{RunID: testRunID, Outcome: sparkwing.Failed, Error: errExecutor}
	if err := r.FinalizeRun(context.Background(), conflict); err == nil {
		t.Fatal("conflicting finalization succeeded")
	}
	if len(executor.finalizes) != 4 {
		t.Fatalf("conflict reached executor; calls = %d", len(executor.finalizes))
	}
}

func TestRunnerCanFinalizeUnknownBeginAfterRestart(t *testing.T) {
	executor := &fakeExecutor{}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	errRun := errors.New("runner restarted after uncertain Begin")
	if err := r.FinalizeRun(context.Background(), corerunner.RunFinalizationRequest{
		RunID: testRunID, Outcome: sparkwing.Failed, Error: errRun,
	}); err != nil {
		t.Fatal(err)
	}
	if len(executor.finalizes) != 1 {
		t.Fatalf("finalize calls = %d, want 1", len(executor.finalizes))
	}
	got := executor.finalizes[0]
	if got.RunID != testRunID || got.Session != "" || got.Outcome != sparkwing.Failed || got.Error != errRun.Error() {
		t.Fatalf("finalize request = %+v", got)
	}
}

func TestRunnerStartupRecoversDurableExecutorBeforeAdmission(t *testing.T) {
	errRecover := errors.New("durable recovery unavailable")
	executor := &fakeExecutor{recoverErr: errRecover}
	if _, err := New(context.Background(), testConfig(), executor); !errors.Is(err, errRecover) {
		t.Fatalf("New error = %v, want recovery failure", err)
	}
	if executor.recovers != 1 || len(executor.begins) != 0 {
		t.Fatalf("recovery calls = %d, begin calls = %d", executor.recovers, len(executor.begins))
	}
}

func TestDurableFinalizationTombstoneSurvivesRunnerRestart(t *testing.T) {
	executor := &fakeExecutor{}
	first, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := first.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	if result := first.RunNode(context.Background(), nodeRequest(validation, "shard-1")); result.Err != nil {
		t.Fatalf("RunNode: %+v", result)
	}
	terminal := corerunner.RunFinalizationRequest{RunID: testRunID, Outcome: sparkwing.Success}
	if err := first.FinalizeRun(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if executor.recovers != 2 {
		t.Fatalf("startup recovery calls = %d, want 2", executor.recovers)
	}
	if err := restarted.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	executions := len(executor.executes)
	if result := restarted.RunNode(context.Background(), nodeRequest(validation, "shard-2")); result.Err == nil {
		t.Fatal("finalized durable RunID reopened after runner restart")
	}
	if len(executor.executes) != executions {
		t.Fatal("finalized durable RunID executed after runner restart")
	}
	if result := executor.Execute(context.Background(), ExecuteRequest{
		Generation: executor.generation, RunID: testRunID,
	}); result.Err == nil {
		t.Fatal("executor accepted a late execution after durable finalization")
	}
	if err := restarted.FinalizeRun(context.Background(), corerunner.RunFinalizationRequest{
		RunID: testRunID, Outcome: sparkwing.Failed, Error: errExecutor,
	}); !corerunner.IsTerminalFinalizationError(err) {
		t.Fatalf("restart conflict error = %v, want terminal finalization refusal", err)
	}
}

func TestRunnerGenerationFencesOverlappingOldProcess(t *testing.T) {
	executor := &fakeExecutor{}
	oldRunner, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := oldRunner.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	newRunner, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if result := oldRunner.RunNode(context.Background(), nodeRequest(validation, "shard-1")); result.Err == nil {
		t.Fatal("old runner executed after the durable generation advanced")
	}
	if err := oldRunner.FinalizeRun(context.Background(), corerunner.RunFinalizationRequest{
		RunID: testRunID, Outcome: sparkwing.Failed, Error: errExecutor,
	}); !corerunner.IsTerminalFinalizationError(err) {
		t.Fatalf("stale finalization error = %v, want terminal generation refusal", err)
	}
	if err := newRunner.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	if result := newRunner.RunNode(context.Background(), nodeRequest(validation, "shard-1")); result.Err != nil {
		t.Fatalf("new generation failed to execute: %+v", result)
	}
}

type drainingGenerationExecutor struct {
	mu             sync.Mutex
	generation     int
	active         int
	idle           chan struct{}
	executeEntered chan struct{}
	executeRelease chan struct{}
	recoverEntered chan struct{}
}

func (e *drainingGenerationExecutor) Recover(ctx context.Context) (string, error) {
	e.mu.Lock()
	active := e.active
	idle := e.idle
	if e.generation != 0 {
		close(e.recoverEntered)
	}
	e.mu.Unlock()
	if active != 0 {
		select {
		case <-idle:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.generation++
	return fmt.Sprintf("generation-%d", e.generation), nil
}

func (e *drainingGenerationExecutor) Begin(_ context.Context, req BeginRequest) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if req.Generation != fmt.Sprintf("generation-%d", e.generation) {
		return "", fmt.Errorf("stale runner generation")
	}
	return "session-capability", nil
}

func (e *drainingGenerationExecutor) Execute(_ context.Context, req ExecuteRequest) corerunner.Result {
	e.mu.Lock()
	if req.Generation != fmt.Sprintf("generation-%d", e.generation) {
		e.mu.Unlock()
		return corerunner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("stale runner generation")}
	}
	e.active++
	e.idle = make(chan struct{})
	e.mu.Unlock()
	close(e.executeEntered)
	<-e.executeRelease
	e.mu.Lock()
	e.active--
	close(e.idle)
	e.mu.Unlock()
	return corerunner.Result{Outcome: sparkwing.Success}
}

func (*drainingGenerationExecutor) Finalize(context.Context, FinalizeRequest) error { return nil }

func TestRunnerRecoveryWaitsForPriorGenerationExecution(t *testing.T) {
	executor := &drainingGenerationExecutor{
		executeEntered: make(chan struct{}),
		executeRelease: make(chan struct{}),
		recoverEntered: make(chan struct{}),
	}
	oldRunner, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := oldRunner.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan corerunner.Result, 1)
	go func() { oldDone <- oldRunner.RunNode(context.Background(), nodeRequest(validation, "shard-1")) }()
	<-executor.executeEntered
	newDone := make(chan error, 1)
	go func() {
		_, err := New(context.Background(), testConfig(), executor)
		newDone <- err
	}()
	<-executor.recoverEntered
	select {
	case err := <-newDone:
		t.Fatalf("recovery acknowledged while old execution was active: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(executor.executeRelease)
	if result := <-oldDone; result.Err != nil {
		t.Fatalf("old execution cleanup result = %+v", result)
	}
	if err := <-newDone; err != nil {
		t.Fatal(err)
	}
}

type executionHoldingExecutor struct {
	executeEntered  chan struct{}
	executeRelease  chan struct{}
	finalizeEntered chan struct{}
}

func (*executionHoldingExecutor) Recover(context.Context) (string, error) {
	return "generation-1", nil
}

func (*executionHoldingExecutor) Begin(context.Context, BeginRequest) (string, error) {
	return "session-capability", nil
}

func (e *executionHoldingExecutor) Execute(context.Context, ExecuteRequest) corerunner.Result {
	close(e.executeEntered)
	<-e.executeRelease
	return corerunner.Result{Outcome: sparkwing.Success}
}

func (e *executionHoldingExecutor) Finalize(context.Context, FinalizeRequest) error {
	close(e.finalizeEntered)
	return nil
}

func TestRunnerFinalizationWaitsForActiveExecution(t *testing.T) {
	executor := &executionHoldingExecutor{
		executeEntered: make(chan struct{}), executeRelease: make(chan struct{}), finalizeEntered: make(chan struct{}),
	}
	r, err := New(context.Background(), testConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	validation := validationRequest(t)
	if err := r.ValidatePlan(context.Background(), validation); err != nil {
		t.Fatal(err)
	}
	executeDone := make(chan corerunner.Result, 1)
	go func() { executeDone <- r.RunNode(context.Background(), nodeRequest(validation, "shard-1")) }()
	<-executor.executeEntered
	finalizeDone := make(chan error, 1)
	go func() {
		finalizeDone <- r.FinalizeRun(context.Background(), corerunner.RunFinalizationRequest{
			RunID: testRunID, Outcome: sparkwing.Success,
		})
	}()
	select {
	case <-executor.finalizeEntered:
		t.Fatal("finalization entered before active execution cleanup")
	case <-time.After(100 * time.Millisecond):
	}
	close(executor.executeRelease)
	if result := <-executeDone; result.Err != nil {
		t.Fatalf("execute result = %+v", result)
	}
	if err := <-finalizeDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.finalizeEntered:
	default:
		t.Fatal("finalization did not run after execution cleanup")
	}
	if result := r.RunNode(context.Background(), nodeRequest(validation, "shard-2")); result.Err == nil {
		t.Fatal("late execution entered after finalization tombstone")
	}
}
