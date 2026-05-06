package sparkwing

import (
	"context"
	"fmt"
	"time"
)

// RunContext is the typed environment every Plan and Job sees.
// Populated by the orchestrator at dispatch time from the trigger
// payload, git state, and cluster metadata.
type RunContext struct {
	// RunID uniquely identifies the overall pipeline run.
	RunID string

	// Pipeline is the registered name of the invoked pipeline
	// (e.g. "lint", "build-test-deploy").
	Pipeline string

	// Git is the run's view of the cloned working tree. Same instance
	// as `Runtime().Git`. Live methods (IsDirty, FilesetHash, …) shell
	// out fresh each call; data fields (SHA, Branch, Repo, RepoURL)
	// are the trigger-time snapshot.
	Git *Git

	// Trigger describes how the run was initiated.
	Trigger TriggerInfo

	// StartedAt is set when the orchestrator begins the run.
	StartedAt time.Time
}

// TriggerInfo describes the trigger that started the run.
type TriggerInfo struct {
	Source string            // "manual", "push", "schedule", "webhook"
	User   string            // invoker identity, when known
	Env    map[string]string // trigger-supplied environment
}

// TriggerEnv returns a trigger-supplied environment variable.
func (r RunContext) TriggerEnv(key string) string {
	if r.Trigger.Env == nil {
		return ""
	}
	return r.Trigger.Env[key]
}

// LogRecord is the structured unit every Logger receives. The
// orchestrator persists records as JSONL on disk. Msg is allowed to
// contain raw ANSI from child processes; envelope fields (Level,
// Event, Node, Job, JobStack) never do.
//
// Job / JobStack form a trace: Job is the innermost frame (typically
// the Node's id) and JobStack is the chain of its ancestors outer →
// inner.
type LogRecord struct {
	TS       time.Time      `json:"ts"`
	Level    string         `json:"level,omitempty"` // "info" | "warn" | "error"
	Node     string         `json:"node,omitempty"`  // set by nodeLogger on writes to disk + delegate
	Job      string         `json:"job,omitempty"`   // innermost Job frame
	JobStack []string       `json:"job_stack,omitempty"`
	Event    string         `json:"event,omitempty"` // "" (plain msg), "node_start", "node_end", "step_start", "step_end", "step_skipped", "retry", "exec_line", "run_plan", "run_summary", "run_finish"
	Msg      string         `json:"msg,omitempty"`
	Attrs    map[string]any `json:"attrs,omitempty"`
}

// Logger is the sink for job output. The orchestrator installs a
// logger into ctx before dispatching each node; sparkwing.Info /
// Warn / Error / Debug emit records through it.
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
	keyRefResolver
	keyJSONRefResolver
	keyPipelineResolver
	keyPipelineAwaiter
	keyJobStack
	keySpawnHandler
	keyInputs
)

// WithLogger returns a derived context carrying the given logger.
func WithLogger(ctx context.Context, l Logger) context.Context {
	return context.WithValue(ctx, keyLogger, l)
}

// LoggerFromContext returns the active logger or a no-op if none is set.
func LoggerFromContext(ctx context.Context) Logger {
	if l, ok := ctx.Value(keyLogger).(Logger); ok {
		return l
	}
	return nopLogger{}
}

// WithNode installs the current node ID into ctx. Exec primitives
// tag their emitted lines with this ID so logs are attributable.
func WithNode(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, keyNode, nodeID)
}

// NodeFromContext returns the currently-executing node ID, or "" if unset.
func NodeFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(keyNode).(string); ok {
		return id
	}
	return ""
}

// WithJob returns a derived context with `name` pushed onto the job
// frame stack.
func WithJob(ctx context.Context, name string) context.Context {
	cur := JobStackFromContext(ctx)
	// Fresh slice so siblings don't share a backing array; ctx values
	// must be immutable from a caller's perspective.
	next := make([]string, len(cur)+1)
	copy(next, cur)
	next[len(cur)] = name
	return context.WithValue(ctx, keyJobStack, next)
}

// JobFromContext returns the innermost Job frame name, or "" if no
// frame has been installed.
func JobFromContext(ctx context.Context) string {
	s := JobStackFromContext(ctx)
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// JobStackFromContext returns the full stack of active Job frames,
// outer → inner, or nil when no frames are active.
func JobStackFromContext(ctx context.Context) []string {
	if s, ok := ctx.Value(keyJobStack).([]string); ok {
		return s
	}
	return nil
}

func recordEnvelope(ctx context.Context, rec LogRecord) LogRecord {
	stack := JobStackFromContext(ctx)
	if len(stack) == 0 {
		return rec
	}
	rec.Job = stack[len(stack)-1]
	if len(stack) > 1 {
		parents := make([]string, len(stack)-1)
		copy(parents, stack[:len(stack)-1])
		rec.JobStack = parents
	}
	return rec
}

// Info emits an info-level message to the active logger.
//
//	sparkwing.Info(ctx, "deployed %s to %s", version, target)
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

func emitLevel(ctx context.Context, level, format string, args ...any) {
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: level,
		Node:  NodeFromContext(ctx),
		Msg:   fmt.Sprintf(format, args...),
	}))
}
