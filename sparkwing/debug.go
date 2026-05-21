package sparkwing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// debugEnabled toggles emission of Level:"debug" LogRecords. Atomic
// so Debug is close to free when off (one load + branch). Seeded
// from SPARKWING_DEBUG at package init; tests can override via the
// test-only setDebug helper in testhelpers_test.go.
var debugEnabled atomic.Bool

func init() {
	debugEnabled.Store(parseDebug(os.Getenv("SPARKWING_DEBUG")))
}

// parseDebug interprets SPARKWING_DEBUG. Empty / "0" / "false" → off;
// any other non-empty value → on.
func parseDebug(v string) bool {
	if v == "" || v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	return true
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
// unconditionally -- the atomic load is negligible.
func DebugEnabled() bool { return debugEnabled.Load() }

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
		JobID: NodeFromContext(ctx),
		Msg:   fmt.Sprintf(format, args...),
	}))
}
