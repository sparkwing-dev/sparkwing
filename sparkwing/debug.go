package sparkwing

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// debugEnabled toggles emission of Level:"debug" LogRecords. Atomic
// so Debug is close to free when off (one load + branch).
var debugEnabled atomic.Bool

func init() {
	debugEnabled.Store(runtime.Debug)
}

// DebugEnabled reports whether SDK-internal verbose logging is on.
// Callers wrapping expensive formatting should guard on this to skip
// the work:
//
//	if sparkwing.DebugEnabled() {
//	    sparkwing.Debug(ctx, "plan hash = %s", expensiveHashOf(plan))
//	}
//
// Cheap format calls (string constants, small ints) can call Debug
// unconditionally — the atomic load is negligible.
func DebugEnabled() bool { return debugEnabled.Load() }

// SetDebug overrides the SPARKWING_DEBUG-derived default. Safe for
// concurrent use.
func SetDebug(on bool) { debugEnabled.Store(on) }

// Debug emits a debug-level LogRecord. No-op when SPARKWING_DEBUG is
// not set (the default). Use it for SDK-internal tracing and
// pipeline-author diagnostics that should stay out of the normal
// output.
//
//	sparkwing.Debug(ctx, "cache lookup: key=%s", key)
func Debug(ctx context.Context, format string, args ...any) {
	if !debugEnabled.Load() {
		return
	}
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "debug",
		Node:  NodeFromContext(ctx),
		Msg:   fmt.Sprintf(format, args...),
	}))
}
