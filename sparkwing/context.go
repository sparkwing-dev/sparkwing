package sparkwing

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// RunContext is the typed environment every Plan and Job sees, populated by
// the orchestrator at dispatch time.
type RunContext struct {
	RunID string

	// Pipeline is the registered name of the invoked pipeline
	// (e.g. "lint", "build-test-deploy").
	Pipeline string

	// Git is the run's view of the cloned working tree. Same instance
	// as `Runtime().Git`. Live methods (IsDirty, FilesetHash, …) shell
	// out fresh each call; data fields (SHA, Branch, Repo, RepoURL)
	// are the trigger-time snapshot.
	Git *Git

	Trigger TriggerInfo

	StartedAt time.Time

	// NoCache reports that this run was asked to ignore cached per-node
	// results; cache writes still happen. Plan code reads the signal here
	// because the orchestrator installs it on the context only after the
	// plan is built.
	NoCache bool

	// DryRun reports that this run was asked to describe its work rather
	// than apply it. A step body reads the same answer from [IsDryRun];
	// this field is what a Plan reads, which runs before the orchestrator
	// installs the mode on the context.
	DryRun bool
}

// TriggerInfo describes the trigger that started the run.
//
// Trigger-supplied values flow through the pipeline's typed Config
// struct. Declare them under the trigger's values: block in
// sparkwing.yaml (e.g. on.push.values) with a matching `sw:"..."`
// tag on a Config struct field, then read via
// sparkwing.PipelineConfig[T](ctx).
type TriggerInfo struct {
	Source string // "manual", "push", "schedule", "webhook"
	User   string // invoker identity, when known

	// PullRequest is non-nil when the run was started by a GitHub
	// pull_request event. RunContext.Git already carries the PR head
	// (SHA, Branch); this adds the base ref and PR number a gate needs
	// to diff against the target branch or post back to the PR.
	PullRequest *PullRequest
}

// PullRequest carries the GitHub pull_request fields a PR gate reads.
type PullRequest struct {
	Number int
	// Action is the pull_request event action that fired the run
	// (opened, synchronize, reopened).
	Action  string
	BaseRef string
	// BaseSHA is the tip commit of the base branch at event time.
	BaseSHA string
	HeadRef string
	// HeadSHA is the PR head commit the run checks out.
	HeadSHA string
}

// Trigger-env keys carrying GitHub pull_request metadata. The
// controller stamps these onto the persisted trigger; the orchestrator
// reads them back via PullRequestFromEnv when it builds the run
// context.
const (
	EnvGitHubEventName = "GITHUB_EVENT_NAME"
	EnvPRNumber        = "GITHUB_PR_NUMBER"
	EnvPRAction        = "GITHUB_PR_ACTION"
	EnvPRBaseRef       = "GITHUB_BASE_REF"
	EnvPRBaseSHA       = "GITHUB_BASE_SHA"
	EnvPRHeadRef       = "GITHUB_HEAD_REF"
	EnvPRHeadSHA       = "GITHUB_HEAD_SHA"
)

// EventPullRequest is the EnvGitHubEventName value that marks a
// pull_request-triggered run.
const EventPullRequest = "pull_request"

// PullRequestFromEnv reconstructs a PullRequest from a trigger's env
// map, or returns nil when the map does not describe a pull_request
// event. A malformed number degrades to zero rather than failing the
// run.
func PullRequestFromEnv(env map[string]string) *PullRequest {
	if env[EnvGitHubEventName] != EventPullRequest {
		return nil
	}
	num, _ := strconv.Atoi(env[EnvPRNumber])
	return &PullRequest{
		Number:  num,
		Action:  env[EnvPRAction],
		BaseRef: env[EnvPRBaseRef],
		BaseSHA: env[EnvPRBaseSHA],
		HeadRef: env[EnvPRHeadRef],
		HeadSHA: env[EnvPRHeadSHA],
	}
}

// LogRecord is the structured unit every Logger receives. The
// orchestrator persists records as JSONL on disk. Msg is allowed to
// contain raw ANSI from child processes; envelope fields (Level,
// Event, JobID, Step) never do.
type LogRecord struct {
	TS    time.Time      `json:"ts"`
	Level string         `json:"level,omitempty"` // "info" | "warn" | "error"
	JobID string         `json:"node,omitempty"`  // the wire tag stays "node" for log-format compatibility
	Step  string         `json:"step,omitempty"`  // active step ID
	Event string         `json:"event,omitempty"` // "" for a plain message; otherwise one of the Event* constants
	Msg   string         `json:"msg,omitempty"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Logger is the sink for job output. The orchestrator installs a
// logger into ctx before dispatching each node, and into the context
// it hands Pipeline.Plan. sparkwing.Info / Warn / Error / Debug emit
// records through it, and a context carrying none discards them.
//
// Log wraps a level+message into a default-event LogRecord; most
// callers reach it indirectly via the per-level package helpers.
// Emit is the primary API for structured events (node boundaries,
// exec-line tagging, summaries).
type Logger interface {
	Log(level, msg string)
	Emit(rec LogRecord)
}

type nopLogger struct{}

func (nopLogger) Log(level, msg string) {}
func (nopLogger) Emit(LogRecord)        {}

type ctxKey int

const (
	keyLogger ctxKey = iota
	keyNode
	keyJSONRefResolver
	keyPipelineResolver
	keyPipelineAwaiter
	keySpawnHandler
	keyInputs
	keyStep
	keyPipelineConfig
	keyPipelineSecrets
	keyResourceReporter
)

// ResourceSample is one measured resource reading for a spawned command:
// the CPU it drew, averaged over its wall-clock span, and the peak resident
// memory of its process subtree.
type ResourceSample struct {
	// CPUMillicores is the command's average CPU draw over its run, in
	// thousandths of a core (1000 == one core busy for the whole span).
	CPUMillicores int64
	// MemoryBytes is the peak resident set size of the command's process
	// subtree, in bytes.
	MemoryBytes int64
	// CPUTime is the raw user+system CPU the command's reaped subtree drew.
	// The same usage lands in the process's RUSAGE_CHILDREN at reap, so the
	// orchestrator subtracts this from the node sampler to count it once.
	CPUTime time.Duration
}

// ResourceReporter absorbs a [ResourceSample] measured when a spawned
// command finishes.
type ResourceReporter func(ResourceSample)

// WithResourceReporter installs fn so that sparkwing.Bash / sparkwing.Exec
// report each finished command's measured CPU and memory. Pipeline authors
// do not call it.
func WithResourceReporter(ctx context.Context, fn ResourceReporter) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, keyResourceReporter, fn)
}

func reportResource(ctx context.Context, sample ResourceSample) {
	if fn, ok := ctx.Value(keyResourceReporter).(ResourceReporter); ok && fn != nil {
		fn(sample)
	}
}

// LoggerFromContext returns the active logger or a no-op if none is set.
func LoggerFromContext(ctx context.Context) Logger {
	if l, ok := ctx.Value(keyLogger).(Logger); ok {
		return l
	}
	return nopLogger{}
}

// NodeFromContext returns the currently-executing node ID, or "" if unset.
func NodeFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(keyNode).(string); ok {
		return id
	}
	return ""
}

// WithStep installs the active step ID into ctx so the breadcrumb on
// records emitted *inside* the step body carries it. A step's own
// start and end events render at the node level instead, so the
// breadcrumb does not repeat the step name.
func WithStep(ctx context.Context, stepID string) context.Context {
	return context.WithValue(ctx, keyStep, stepID)
}

// StepFromContext returns the active step ID, or "" outside a step.
func StepFromContext(ctx context.Context) string {
	if s, ok := ctx.Value(keyStep).(string); ok {
		return s
	}
	return ""
}

func recordEnvelope(ctx context.Context, rec LogRecord) LogRecord {
	if rec.Step == "" {
		rec.Step = StepFromContext(ctx)
	}
	return rec
}

// Info emits an info-level message to the active logger.
func Info(ctx context.Context, format string, args ...any) {
	emitLevel(ctx, "info", format, args...)
}

// Warn emits a warn-level message.
func Warn(ctx context.Context, format string, args ...any) {
	emitLevel(ctx, "warn", format, args...)
}

// Error emits an error-level message.
func Error(ctx context.Context, format string, args ...any) {
	emitLevel(ctx, "error", format, args...)
}

// Annotate records a persistent, human-readable summary string on the
// currently-executing Job. Unlike Info, which writes to the run log,
// annotations are stored on the Job row itself and surface on the
// dashboard alongside the node's status -- a place for the step to
// say "processed 1,234 records · 12 failed" without an operator
// having to dig through logs.
//
// Multiple calls within a node accumulate; the orchestrator appends
// each message to the node's annotations list in call order. Outside
// a node context (no logger installed, or no node ID in ctx) Annotate
// is a no-op, matching the Info/Warn/Error convention.
func Annotate(ctx context.Context, msg string) {
	if NodeFromContext(ctx) == "" {
		return
	}
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: NodeFromContext(ctx),
		Event: EventNodeAnnotation,
		Msg:   msg,
		Attrs: map[string]any{"message": msg},
	}))
}

// EventNodeAnnotation is the LogRecord.Event value emitted by
// Annotate. Persistence layers observing the log stream should
// dispatch on this constant rather than the raw string.
const EventNodeAnnotation = "node_annotation"

// Summary records a persistent markdown run summary on the
// currently-executing Job or Step. Unlike Annotate, which appends a
// short scannable line, Summary stores a larger overwrite-on-write
// markdown blob.
//
// Multiple calls within the same scope keep only the last value: the
// later call replaces the earlier one. Summaries fired inside a step
// body land on that step's row; summaries fired between steps (or
// before any step starts) land on the node row. Outside a node
// context (no logger installed, or no node ID in ctx) Summary is a
// no-op, matching the Info/Warn/Error convention.
//
// The markdown is stored opaquely; the dashboard sanitizes and
// renders later.
func Summary(ctx context.Context, markdown string) {
	if NodeFromContext(ctx) == "" {
		return
	}
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: NodeFromContext(ctx),
		Event: EventNodeSummary,
		Msg:   markdown,
		Attrs: map[string]any{"markdown": markdown},
	}))
}

// EventNodeSummary is the LogRecord.Event value emitted by Summary.
// Persistence layers observing the log stream should dispatch on
// this constant rather than the raw string. Overwrite-on-write
// semantics: the last record per (node, step) scope wins.
const EventNodeSummary = "node_summary"

// Per-step lifecycle events. Emitted by the Work-runner before / after
// each step body. EventStepSkipped fires for skipIf / dry-run guards
// that short-circuit the step before its body runs. EventWorkFailFast
// summarizes the decisive failure and sibling cancellation latency.
const (
	EventStepStart    = "step_start"
	EventStepEnd      = "step_end"
	EventStepSkipped  = "step_skipped"
	EventWorkFailFast = "work_fail_fast"
)

func emitLevel(ctx context.Context, level, format string, args ...any) {
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: level,
		JobID: NodeFromContext(ctx),
		Msg:   fmt.Sprintf(format, args...),
	}))
}
