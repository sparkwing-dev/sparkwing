package orchestrator

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/diskspace"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: returns a new map because run_start's base is the invocation
// snapshot the store already holds as Run.Invocation. A root that names no
// readable volume -- a run whose state is remote carries none -- leaves the
// attrs off rather than reporting zero free, which reads as a full disk.
func withDiskAttrs(attrs map[string]any, root string) map[string]any {
	out := make(map[string]any, len(attrs)+3)
	for key, value := range attrs {
		out[key] = value
	}
	if free, total, ok := diskspace.Usage(root); ok {
		out["disk_free_bytes"] = free
		out["disk_total_bytes"] = total
		out["disk_path"] = root
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func emitNodeStart(log sparkwing.Logger, ts time.Time, diskRoot string, attrs map[string]any) {
	log.Emit(sparkwing.LogRecord{
		TS:    ts,
		Level: "info",
		Event: "node_start",
		Attrs: withDiskAttrs(attrs, diskRoot),
	})
}
