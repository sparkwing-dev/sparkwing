package wingd

import (
	"net/url"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func (d *Daemon) recordJournal(kind string, c *conn, data map[string]any) {
	if d.journal == nil || (c != nil && c.healthProbe) {
		return
	}
	r := journal.Record{Kind: kind, Data: data}
	if c != nil {
		r.RunID, r.DisplayRunID, r.Pipeline, r.Repo, r.PID = c.runID, c.displayRunID, c.pipeline, c.repo, c.pid
		if r.RunID == "" {
			r.RunID = c.journalRunID
		}
		connection := kind == "connection_opened" || kind == "connection_handshake" || kind == "connection_closed" || kind == "handshake_refused" || kind == "message_refused"
		if c.ownerRunID != "" || connection {
			if r.Data == nil {
				r.Data = make(map[string]any)
			}
		}
		if c.ownerRunID != "" {
			r.Data["owner_run_id"] = c.ownerRunID
		}
		if connection {
			r.PID = c.peerPID
			r.Data["connection_id"] = c.id
		}
	}
	d.journal.Enqueue(r)
}

func journalBudget(b Budget) map[string]any {
	return map[string]any{"cores": b.Cores, "cores_fraction": b.CoresFraction, "memory_bytes": b.MemoryBytes, "memory_fraction": b.MemoryFraction, "enforce": b.Enforce, "ignore_external": b.IgnoreExternal, "raw": b.Raw}
}

func journalPolicy(p AdmissionPolicy) map[string]any {
	s := p.Scheduling
	j := p.Jev
	scheduling := map[string]any{"aging_every": s.AgingEvery, "burst": map[string]any{"max_cores": s.Burst.MaxCores, "max_p99": s.Burst.MaxP99, "min_samples": s.Burst.MinSamples}}
	if len(s.BackfillDelay) > 0 {
		scheduling["backfill_delay"] = s.BackfillDelay
	}
	if len(s.ClassWeight) > 0 {
		scheduling["class_weight"] = s.ClassWeight
	}
	return map[string]any{
		"mode":       p.Mode,
		"scheduling": scheduling,
		"jev":        map[string]any{"model": j.Model, "endpoint": journalEndpoint(j.Endpoint), "timeout": j.Timeout, "min_confidence": j.MinConfidence, "min_probability": j.MinProbability, "max_backfill": j.MaxBackfill},
	}
}

func journalEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

func (d *Daemon) recordWindow(now time.Time, ev admissionEvent, c *conn, data map[string]any) {
	d.events.record(now, ev)
	if data == nil {
		data = map[string]any{}
	}
	if ev.Key != "" {
		data["key"] = ev.Key
	}
	if ev.Kind == eventGrant {
		data["wait_ms"] = ev.WaitMS
	}
	if ev.BackfillCount != 0 {
		data["backfill_count"] = ev.BackfillCount
	}
	d.recordJournal(ev.Kind, c, data)
}
