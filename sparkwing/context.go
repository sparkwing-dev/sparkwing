package sparkwing

import (
	"context"
	"fmt"
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

	// NoMemo reports that this run was asked to ignore cached per-node
	// results; cache writes still happen. Plan code reads the signal here
	// because the orchestrator installs it on the context only after the
	// plan is built.
	NoMemo bool

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

// LogRecord is the structured unit every Logger receives. The
// orchestrator persists records as JSONL on disk. Msg is allowed to
// contain raw ANSI from child processes; envelope fields (Level,
// Event, JobID, Step) never do.
type LogRecord struct {
	TS    time.Time      `json:"ts"`
	Level string         `json:"level,omitempty"` // "info" | "warn" | "error"
	JobID string         `json:"job,omitempty"`
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
	keyInputs
	keyStep
	keyPipelineConfig
	keyPipelineSecrets
	keyResourceReporter
)

// ResourceSample is one measured resource reading for a spawned command:
// the CPU it drew, averaged over its wall-clock span, and the peak resident
// memory of its process subtree.
type resourceSample struct {
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
type resourceReporter func(resourceSample)

// WithResourceReporter installs fn so that sparkwing.Bash / sparkwing.Exec
// report each finished command's measured CPU and memory. Pipeline authors
// do not call it.
func withResourceReporter(ctx context.Context, fn resourceReporter) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, keyResourceReporter, fn)
}

func reportResource(ctx context.Context, sample resourceSample) {
	if fn, ok := ctx.Value(keyResourceReporter).(resourceReporter); ok && fn != nil {
		fn(sample)
	}
}

// LoggerFromContext returns the active logger or a no-op if none is set.
func loggerFromContext(ctx context.Context) Logger {
	if l, ok := ctx.Value(keyLogger).(Logger); ok {
		return l
	}
	return nopLogger{}
}

// NodeFromContext returns the currently-executing node ID, or "" if unset.
func nodeFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(keyNode).(string); ok {
		return id
	}
	return ""
}

// WithStep installs the active step ID into ctx so the breadcrumb on
// records emitted *inside* the step body carries it. A step's own
// start and end events render at the node level instead, so the
// breadcrumb does not repeat the step name.
func withStep(ctx context.Context, stepID string) context.Context {
	return context.WithValue(ctx, keyStep, stepID)
}

// StepFromContext returns the active step ID, or "" outside a step.
func stepFromContext(ctx context.Context) string {
	if s, ok := ctx.Value(keyStep).(string); ok {
		return s
	}
	return ""
}

func recordEnvelope(ctx context.Context, rec LogRecord) LogRecord {
	if rec.Step == "" {
		rec.Step = stepFromContext(ctx)
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

// Summary emits markdown for the active job or step. The host masks and
// bounds the record before storing it.
func Summary(ctx context.Context, markdown string) {
	if nodeFromContext(ctx) == "" {
		return
	}
	loggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: nodeFromContext(ctx),
		Event: eventSummary,
		Msg:   markdown,
		Attrs: map[string]any{"markdown": markdown},
	}))
}

// eventSummary is the LogRecord.Event value emitted by Summary.
// Persistence layers observing the log stream should dispatch on
// this constant rather than the raw string. Overwrite-on-write
// semantics: the last record per (node, step) scope wins.
const eventSummary = "summary"

func emitLevel(ctx context.Context, level, format string, args ...any) {
	loggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: level,
		JobID: nodeFromContext(ctx),
		Msg:   fmt.Sprintf(format, args...),
	}))
}
