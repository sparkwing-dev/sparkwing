package nodehost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: an event records something that already happened, so a store that
// refuses one is logged and never changes what the node or run does, and the
// write outlives the run's cancel so a cancelled node still records it.
func hostEvent(ctx context.Context, run *Run, nodeID, kind string, payload []byte) {
	if err := run.State.AppendEvent(context.WithoutCancel(ctx), run.ID, nodeID, kind, payload); err != nil {
		run.Logger.Warn("nodehost: append event", "run", run.ID, "node", nodeID, "kind", kind, "err", err)
	}
}

// safety: nil fields store no payload, as the dispatcher writes a bare event.
func hostEventJSON(ctx context.Context, run *Run, nodeID, kind string, fields map[string]any) {
	if fields == nil {
		hostEvent(ctx, run, nodeID, kind, nil)
		return
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		run.Logger.Warn("nodehost: encode event", "run", run.ID, "node", nodeID, "kind", kind, "err", err)
		return
	}
	hostEvent(ctx, run, nodeID, kind, payload)
}

// safety: an attempt holds the node log only while it runs, so a note written
// between attempts opens the log again; OpenLog appends.
func hostNodeLog(ctx context.Context, cfg *Config, n *NodeRun, rec sparkwing.LogRecord) {
	if cfg == nil || cfg.Attempts == nil || cfg.Attempts.OpenLog == nil {
		return
	}
	a := cfg.Attempts
	run, id := n.Run, n.Plan.ID
	nlog, err := a.OpenLog(ctx, run.ID, id)
	if err != nil {
		run.Logger.Warn("nodehost: open node log", "run", run.ID, "node", id, "err", err)
		return
	}
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	rec.Msg = a.Masker.Mask(rec.Msg)
	rec.Attrs = a.Masker.MaskAttrs(rec.Attrs)
	nlog.Emit(rec)
	if err := nlog.Close(); err != nil {
		run.Logger.Warn("nodehost: close node log", "run", run.ID, "node", id, "err", err)
	}
}

// safety: the kinds and payloads are the dispatcher's, so the dashboard and
// `runs logs --events-only` read a hosted node's end as they read a dispatched
// one. A cached node's event is the memo's own.
func nodeEndEvent(ctx context.Context, run *Run, id string, res Result) {
	text := []byte(recordedErrText(res.Err))
	switch res.Outcome {
	case sparkwing.Success:
		hostEvent(ctx, run, id, "node_succeeded", nil)
	case sparkwing.Failed:
		hostEvent(ctx, run, id, "node_failed", text)
	case sparkwing.Cancelled:
		hostEvent(ctx, run, id, "node_cancelled", text)
	case sparkwing.Skipped:
		hostEvent(ctx, run, id, "node_skipped", text)
	case sparkwing.SkippedConcurrent:
		hostEvent(ctx, run, id, "node_skipped_concurrent", nil)
	case sparkwing.Superseded:
		hostEvent(ctx, run, id, "node_superseded", text)
	}
}
