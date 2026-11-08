package wingd

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func (d *Daemon) recordJournal(kind string, c *conn, data map[string]any) {
	if c != nil && (kind == "queued" || kind == "grant") {
		if kind == "queued" {
			data["wait_ms"] = max(int64(0), d.now().Sub(c.startAt).Milliseconds())
		}
		if observer, ok := d.cfg.Runs.(interface {
			RecordAdmissionEvent(string, string, map[string]any) error
		}); ok {
			if err := observer.RecordAdmissionEvent(c.runID, kind, data); err != nil {
				d.cfg.logf("run admission event: %v", err)
			}
		}
	}
	if d.journal == nil {
		return
	}
	r := journal.Record{Kind: kind, Data: data}
	if c != nil {
		r.RunID, r.DisplayRunID, r.Pipeline, r.Repo, r.PID = c.runID, c.displayRunID, c.pipeline, c.repo, c.pid
		if r.RunID == "" {
			r.RunID = c.journalRunID
		}
		if c.ownerRunID != "" {
			if r.Data == nil {
				r.Data = make(map[string]any)
			}
			r.Data["owner_run_id"] = c.ownerRunID
		}
	}
	d.journal.Enqueue(r)
}

func journalBudget(b userconfig.Budget) map[string]any {
	return map[string]any{"cores": b.Cores, "cores_fraction": b.CoresFraction, "memory_bytes": b.MemoryBytes, "memory_fraction": b.MemoryFraction, "enforce": b.Enforce, "ignore_external": b.IgnoreExternal, "raw": b.Raw}
}

func journalPolicy(p AdmissionPolicy) map[string]any {
	s := p.Scheduling
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
	}
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
