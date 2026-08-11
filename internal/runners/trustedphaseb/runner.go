package trustedphaseb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sync"
	"time"

	corerunner "github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

const (
	requiredNodes       = 8
	requiredCapacity    = 4
	requiredCores       = 0.75
	requiredMemoryBytes = int64(640 * 1024 * 1024)
	executorCallTimeout = 30 * time.Second
)

type NodePolicy struct {
	ID    string
	Deps  []string
	Steps []string
}

type Config struct {
	Pipeline    string
	Repo        string
	ProfileName string
	Group       string
	Capacity    int
	Scope       sparkwing.Scope
	Cores       float64
	MemoryBytes int64
	Args        map[string]string
	Nodes       []NodePolicy
}

type BeginRequest struct {
	Generation       string
	RunID            string
	Pipeline         string
	Repo             string
	CommitSHA        string
	TriggerSource    string
	TriggerUser      string
	ProfileName      string
	ProfileIsLocal   bool
	Projection       []byte
	ProjectionDigest string
	Args             map[string]string
}

type ExecuteRequest struct {
	Generation       string
	Session          string
	RunID            string
	Pipeline         string
	Repo             string
	CommitSHA        string
	ProfileName      string
	ProjectionDigest string
	NodeID           string
	Ordinal          int
}

type Executor interface {
	// Recover is the executor startup authority for durable sessions owned by
	// this runner. It must reconcile every terminal tombstone whose cleanup was
	// not acknowledged and abort every unfinished session left by an expired
	// runner lease before accepting new work.
	// Recover atomically advances the durable runner generation, fences every
	// prior generation, and waits for prior-generation cleanup before returning.
	// Every later operation carrying a stale generation must fail before work.
	Recover(context.Context) (string, error)
	// Begin is a durable compare-and-set keyed by RunID and the complete
	// request. Identical retries return the same session; conflicts fail.
	// A finalized RunID has an irreversible tombstone and must never create
	// or reopen a session.
	Begin(context.Context, BeginRequest) (string, error)
	// Execute is a durable compare-and-set keyed by session and ordinal.
	// Terminal retries return the recorded result without rerunning, and a
	// finalized RunID refuses every later execution attempt.
	Execute(context.Context, ExecuteRequest) corerunner.Result
	// Finalize durably closes or aborts the session by RunID and is
	// idempotent. Session may be empty when Begin's response was uncertain;
	// the durable RunID CAS remains the cleanup authority. It must persist the
	// irreversible tombstone before acknowledging and reconcile unfinished
	// cleanup after its own restart. Stale-generation and conflicting-tombstone
	// refusals must implement corerunner's terminal-finalization error marker.
	Finalize(context.Context, FinalizeRequest) error
}

type FinalizeRequest struct {
	Generation string
	Session    string
	RunID      string
	Outcome    sparkwing.Outcome
	Error      string
}

type acceptedRun struct {
	begin             BeginRequest
	beginMu           sync.Mutex
	session           string
	finalMu           sync.Mutex
	finalRequest      *FinalizeRequest
	finalAcknowledged bool
	lifecycleMu       sync.Mutex
	activeDispatches  int
	idle              chan struct{}
	finalizing        bool
	pipeline          string
	repo              string
	commitSHA         string
	triggerSource     string
	triggerUser       string
	profileName       string
	profileIsLocal    bool
	projectionDigest  string
	args              map[string]string
}

type Runner struct {
	config     Config
	executor   Executor
	generation string

	mu        sync.Mutex
	accepted  map[string]*acceptedRun
	ordinals  map[string]int
	configErr error
}

func New(ctx context.Context, config Config, executor Executor) (*Runner, error) {
	ordinals := make(map[string]int, len(config.Nodes))
	var configErr error
	if config.Pipeline == "" || config.Repo == "" || config.ProfileName == "" || config.Group == "" {
		configErr = fmt.Errorf("trusted phase-b: configured identity fields must be nonempty")
	}
	if len(config.Nodes) != requiredNodes || config.Capacity != requiredCapacity || config.Scope != sparkwing.ScopeBox || config.Cores != requiredCores || config.MemoryBytes != requiredMemoryBytes {
		configErr = fmt.Errorf("trusted phase-b: configured admission envelope must be 8 nodes at capacity 4, 0.75 cores and 640 MiB each in box scope")
	}
	for index, node := range config.Nodes {
		if node.ID == "" {
			configErr = fmt.Errorf("trusted phase-b: configured node %d has an empty id", index+1)
		}
		if _, exists := ordinals[node.ID]; exists {
			configErr = fmt.Errorf("trusted phase-b: configured node id %q is duplicated", node.ID)
		}
		ordinals[node.ID] = index + 1
	}
	runner := &Runner{
		config:    config,
		executor:  executor,
		accepted:  make(map[string]*acceptedRun),
		ordinals:  ordinals,
		configErr: configErr,
	}
	if configErr != nil {
		return nil, configErr
	}
	if executor == nil {
		return runner, nil
	}
	recoverCtx, cancel := context.WithTimeout(ctx, executorCallTimeout)
	defer cancel()
	generation, err := executor.Recover(recoverCtx)
	if err != nil {
		return nil, fmt.Errorf("trusted phase-b: recover durable executor sessions: %w", err)
	}
	if generation == "" {
		return nil, fmt.Errorf("trusted phase-b: executor returned an empty runner generation")
	}
	runner.generation = generation
	return runner, nil
}

var (
	_ corerunner.Runner        = (*Runner)(nil)
	_ corerunner.PlanValidator = (*Runner)(nil)
	_ corerunner.RunFinalizer  = (*Runner)(nil)
)

func (r *Runner) ValidatePlan(ctx context.Context, req corerunner.PlanValidationRequest) error {
	if r.configErr != nil {
		return r.configErr
	}
	if err := r.validateIdentity(req); err != nil {
		return err
	}
	if err := r.validateProjection(req.Projection, req.RunContext.RunID); err != nil {
		return err
	}
	if r.executor == nil {
		return fmt.Errorf("trusted phase-b: executor is nil")
	}

	begin := BeginRequest{
		Generation:       r.generation,
		RunID:            req.RunContext.RunID,
		Pipeline:         req.RunContext.Pipeline,
		Repo:             req.RunContext.Git.Repo,
		CommitSHA:        req.RunContext.Git.SHA,
		TriggerSource:    req.RunContext.Trigger.Source,
		TriggerUser:      req.RunContext.Trigger.User,
		ProfileName:      req.ProfileName,
		ProfileIsLocal:   req.ProfileIsLocal,
		Projection:       append([]byte(nil), req.Projection...),
		ProjectionDigest: req.ProjectionDigest,
		Args:             cloneStrings(req.Args),
	}
	accepted := &acceptedRun{
		begin:            begin,
		pipeline:         begin.Pipeline,
		repo:             begin.Repo,
		commitSHA:        begin.CommitSHA,
		triggerSource:    begin.TriggerSource,
		triggerUser:      begin.TriggerUser,
		profileName:      begin.ProfileName,
		profileIsLocal:   begin.ProfileIsLocal,
		projectionDigest: begin.ProjectionDigest,
		args:             cloneStrings(begin.Args),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.accepted[begin.RunID]; exists {
		return fmt.Errorf("trusted phase-b: run %q was already admitted", begin.RunID)
	}
	r.accepted[begin.RunID] = accepted
	return nil
}

func (r *Runner) RunNode(ctx context.Context, req corerunner.Request) corerunner.Result {
	r.mu.Lock()
	accepted, ok := r.accepted[req.RunID]
	r.mu.Unlock()
	if !ok {
		return refused("run was not admitted")
	}
	ordinal, ok := r.ordinals[req.NodeID]
	if !ok {
		return refused(fmt.Sprintf("node %q is outside the accepted target set", req.NodeID))
	}
	if req.Pipeline != accepted.pipeline || req.PlanDigest != accepted.projectionDigest {
		return refused("pipeline or projection digest changed after admission")
	}
	if req.Git == nil || req.Git.Repo != accepted.repo || req.Git.SHA != accepted.commitSHA {
		return refused("Git identity changed after admission")
	}
	if req.Trigger.Source != accepted.triggerSource || req.Trigger.User != accepted.triggerUser {
		return refused("trigger identity changed after admission")
	}
	if req.ProfileName != accepted.profileName || req.ProfileIsLocal != accepted.profileIsLocal {
		return refused("profile identity changed after admission")
	}
	if !reflect.DeepEqual(req.Args, accepted.args) {
		return refused("arguments changed after admission")
	}
	if !accepted.startDispatch() {
		return refused("run finalization already started")
	}
	defer accepted.finishDispatch()
	session, err := r.ensureSession(ctx, accepted)
	if err != nil {
		return corerunner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("trusted phase-b: begin executor session: %w", err),
		}
	}
	return r.executor.Execute(ctx, ExecuteRequest{
		Generation:       r.generation,
		Session:          session,
		RunID:            req.RunID,
		Pipeline:         accepted.pipeline,
		Repo:             accepted.repo,
		CommitSHA:        accepted.commitSHA,
		ProfileName:      accepted.profileName,
		ProjectionDigest: accepted.projectionDigest,
		NodeID:           req.NodeID,
		Ordinal:          ordinal,
	})
}

func (r *Runner) FinalizeRun(ctx context.Context, req corerunner.RunFinalizationRequest) error {
	if r.executor == nil {
		return fmt.Errorf("trusted phase-b: executor is nil")
	}
	r.mu.Lock()
	accepted, ok := r.accepted[req.RunID]
	r.mu.Unlock()
	if !ok {
		return r.finalizeExecutor(ctx, FinalizeRequest{
			Generation: r.generation, RunID: req.RunID, Outcome: req.Outcome, Error: errorText(req.Error),
		})
	}
	accepted.lifecycleMu.Lock()
	accepted.finalizing = true
	idle := accepted.idle
	accepted.lifecycleMu.Unlock()
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return fmt.Errorf("trusted phase-b: wait for active execution cleanup: %w", ctx.Err())
		}
	}
	accepted.beginMu.Lock()
	session := accepted.session
	accepted.beginMu.Unlock()
	want := FinalizeRequest{
		Generation: r.generation, Session: session, RunID: req.RunID, Outcome: req.Outcome, Error: errorText(req.Error),
	}
	accepted.finalMu.Lock()
	defer accepted.finalMu.Unlock()
	if accepted.finalRequest != nil {
		prior := *accepted.finalRequest
		if prior.RunID != want.RunID || prior.Outcome != want.Outcome || prior.Error != want.Error {
			return corerunner.TerminalFinalizationError(fmt.Errorf("trusted phase-b: conflicting finalization for run %q", req.RunID))
		}
		if accepted.finalAcknowledged {
			return nil
		}
		if prior.Session != "" {
			want.Session = prior.Session
		}
	} else {
		copy := want
		accepted.finalRequest = &copy
	}
	if err := r.finalizeExecutor(ctx, want); err != nil {
		return err
	}
	accepted.finalAcknowledged = true
	r.mu.Lock()
	if r.accepted[req.RunID] == accepted {
		delete(r.accepted, req.RunID)
	}
	r.mu.Unlock()
	return nil
}

func (a *acceptedRun) startDispatch() bool {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.finalizing {
		return false
	}
	if a.activeDispatches == 0 {
		a.idle = make(chan struct{})
	}
	a.activeDispatches++
	return true
}

func (a *acceptedRun) finishDispatch() {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.activeDispatches--
	if a.activeDispatches == 0 {
		close(a.idle)
		a.idle = nil
	}
}

func (r *Runner) ensureSession(ctx context.Context, accepted *acceptedRun) (string, error) {
	accepted.beginMu.Lock()
	defer accepted.beginMu.Unlock()
	if accepted.session != "" {
		return accepted.session, nil
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), executorCallTimeout)
	defer cancel()
	session, err := r.executor.Begin(callCtx, accepted.begin)
	if err != nil {
		return "", err
	}
	if session == "" {
		return "", fmt.Errorf("executor returned an empty session capability")
	}
	accepted.session = session
	return session, nil
}

func (r *Runner) finalizeExecutor(ctx context.Context, req FinalizeRequest) error {
	callCtx, cancel := context.WithTimeout(ctx, executorCallTimeout)
	defer cancel()
	return r.executor.Finalize(callCtx, req)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func refused(reason string) corerunner.Result {
	return corerunner.Result{
		Outcome: sparkwing.Failed,
		Err:     fmt.Errorf("trusted phase-b: %s", reason),
	}
}

func (r *Runner) validateIdentity(req corerunner.PlanValidationRequest) error {
	if req.RunContext.RunID == "" || req.RunContext.Pipeline != r.config.Pipeline {
		return fmt.Errorf("trusted phase-b: run or pipeline identity mismatch")
	}
	if req.RunContext.Git == nil || req.RunContext.Git.Repo != r.config.Repo || !fullCommitSHA.MatchString(req.RunContext.Git.SHA) {
		return fmt.Errorf("trusted phase-b: Git identity mismatch")
	}
	if req.RunContext.Trigger.Source != "push" || req.RunContext.Trigger.User == "" {
		return fmt.Errorf("trusted phase-b: trigger identity mismatch")
	}
	if req.ProfileName != r.config.ProfileName || !req.ProfileIsLocal {
		return fmt.Errorf("trusted phase-b: profile identity mismatch")
	}
	if !reflect.DeepEqual(req.Args, r.config.Args) {
		return fmt.Errorf("trusted phase-b: argument set mismatch")
	}
	if req.ProjectionDigest == "" || req.ProjectionDigest != projectionDigest(req.Projection) {
		return fmt.Errorf("trusted phase-b: projection digest mismatch")
	}
	return nil
}

type projection struct {
	Pipeline  string            `json:"pipeline"`
	RunID     string            `json:"run_id"`
	Priority  int               `json:"priority,omitempty"`
	Nodes     []projectionNode  `json:"nodes"`
	PlanConc  *json.RawMessage  `json:"plan_concurrency,omitempty"`
	PlanConcs []json.RawMessage `json:"plan_concurrency_groups,omitempty"`
	Resources *json.RawMessage  `json:"plan_resources,omitempty"`
	Secrets   map[string]any    `json:"secrets,omitempty"`
}

type projectionNode struct {
	ID          string              `json:"id"`
	Deps        []string            `json:"deps"`
	Env         map[string]string   `json:"env,omitempty"`
	Groups      []string            `json:"groups,omitempty"`
	Dynamic     bool                `json:"dynamic,omitempty"`
	Approval    *json.RawMessage    `json:"approval,omitempty"`
	OnFailureOf string              `json:"on_failure_of,omitempty"`
	Modifiers   projectionModifiers `json:"modifiers"`
	Work        projectionWork      `json:"work"`
}

type projectionModifiers struct {
	Retry               int      `json:"retry,omitempty"`
	RetryBackoffMS      int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto           bool     `json:"retry_auto,omitempty"`
	TimeoutMS           int64    `json:"timeout_ms,omitempty"`
	RunsOn              []string `json:"runs_on,omitempty"`
	Prefers             []string `json:"prefers,omitempty"`
	WhenRunner          []string `json:"when_runner,omitempty"`
	Cache               bool     `json:"cache,omitempty"`
	CacheTTLMS          int64    `json:"cache_ttl_ms,omitempty"`
	ConcGroup           string   `json:"conc_group,omitempty"`
	ConcCapacity        int      `json:"conc_capacity,omitempty"`
	ConcCost            int      `json:"conc_cost,omitempty"`
	ConcScope           string   `json:"conc_scope,omitempty"`
	ConcOnLimit         string   `json:"conc_on_limit,omitempty"`
	ConcQueueTimeoutMS  int64    `json:"conc_queue_timeout_ms,omitempty"`
	ConcCancelTimeoutMS int64    `json:"conc_cancel_timeout_ms,omitempty"`
	ResCores            float64  `json:"res_cores,omitempty"`
	ResMemoryBytes      int64    `json:"res_memory_bytes,omitempty"`
	Inline              bool     `json:"inline,omitempty"`
	Optional            bool     `json:"optional,omitempty"`
	ContinueOnError     bool     `json:"continue_on_error,omitempty"`
	OnFailure           string   `json:"on_failure,omitempty"`
	HasBeforeRun        bool     `json:"has_before_run,omitempty"`
	HasAfterRun         bool     `json:"has_after_run,omitempty"`
	HasSkipIf           bool     `json:"has_skip_if,omitempty"`
}

type projectionWork struct {
	Steps         []projectionStep  `json:"steps,omitempty"`
	Spawns        []json.RawMessage `json:"spawns,omitempty"`
	SpawnEach     []json.RawMessage `json:"spawn_each,omitempty"`
	StepGroups    []json.RawMessage `json:"step_groups,omitempty"`
	ResultStep    string            `json:"result_step,omitempty"`
	FailurePolicy string            `json:"failure_policy,omitempty"`
}

type projectionStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`
	Finally   bool     `json:"finally,omitempty"`
	Risks     []string `json:"risks,omitempty"`
}

func (r *Runner) validateProjection(raw []byte, runID string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var got projection
	if err := decoder.Decode(&got); err != nil {
		return fmt.Errorf("trusted phase-b: decode projection: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trusted phase-b: projection has trailing JSON")
	}
	if got.Pipeline != r.config.Pipeline || got.RunID != runID || got.Priority != 0 || got.PlanConc != nil || len(got.PlanConcs) != 0 || got.Resources != nil || len(got.Secrets) != 0 {
		return fmt.Errorf("trusted phase-b: projection-level policy mismatch")
	}
	if len(got.Nodes) != len(r.config.Nodes) || len(got.Nodes) == 0 {
		return fmt.Errorf("trusted phase-b: node count %d, want %d", len(got.Nodes), len(r.config.Nodes))
	}
	seen := make(map[string]bool, len(got.Nodes))
	byID := make(map[string]projectionNode, len(got.Nodes))
	for _, node := range got.Nodes {
		if node.ID == "" || seen[node.ID] {
			return fmt.Errorf("trusted phase-b: duplicate or empty node id %q", node.ID)
		}
		seen[node.ID] = true
		byID[node.ID] = node
	}
	for _, policy := range r.config.Nodes {
		node, ok := byID[policy.ID]
		if !ok {
			return fmt.Errorf("trusted phase-b: required node %q is absent", policy.ID)
		}
		if err := r.validateNode(policy, node); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) validateNode(policy NodePolicy, node projectionNode) error {
	modifiers := node.Modifiers
	if !equalStrings(node.Deps, policy.Deps) || len(node.Env) != 0 || len(node.Groups) != 0 || node.Dynamic || node.Approval != nil || node.OnFailureOf != "" {
		return fmt.Errorf("trusted phase-b: node %q identity or graph policy mismatch", node.ID)
	}
	if modifiers.Retry != 0 || modifiers.RetryBackoffMS != 0 || modifiers.RetryAuto || modifiers.TimeoutMS != 0 || len(modifiers.RunsOn) != 0 || len(modifiers.Prefers) != 0 || len(modifiers.WhenRunner) != 0 || modifiers.Cache || modifiers.CacheTTLMS != 0 || modifiers.Inline || modifiers.Optional || modifiers.ContinueOnError || modifiers.OnFailure != "" || modifiers.HasBeforeRun || modifiers.HasAfterRun || modifiers.HasSkipIf {
		return fmt.Errorf("trusted phase-b: node %q enables a forbidden modifier", node.ID)
	}
	if modifiers.ConcGroup != r.config.Group || modifiers.ConcCapacity != r.config.Capacity || modifiers.ConcCost != 1 || modifiers.ConcScope != string(r.config.Scope) || modifiers.ConcOnLimit != string(sparkwing.Queue) || modifiers.ConcQueueTimeoutMS != 0 || modifiers.ConcCancelTimeoutMS != 0 {
		return fmt.Errorf("trusted phase-b: node %q concurrency policy mismatch", node.ID)
	}
	if modifiers.ResCores != r.config.Cores || modifiers.ResMemoryBytes != r.config.MemoryBytes {
		return fmt.Errorf("trusted phase-b: node %q resource policy mismatch", node.ID)
	}
	if len(node.Work.Spawns) != 0 || len(node.Work.SpawnEach) != 0 || len(node.Work.StepGroups) != 0 || node.Work.ResultStep != "" || node.Work.FailurePolicy != "" || len(node.Work.Steps) != len(policy.Steps) {
		return fmt.Errorf("trusted phase-b: node %q work policy mismatch", node.ID)
	}
	for index, step := range node.Work.Steps {
		if step.ID != policy.Steps[index] || len(step.Needs) != 0 || step.IsResult || step.HasSkipIf || step.Finally || len(step.Risks) != 0 {
			return fmt.Errorf("trusted phase-b: node %q step policy mismatch", node.ID)
		}
	}
	return nil
}

func projectionDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func cloneStrings(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
