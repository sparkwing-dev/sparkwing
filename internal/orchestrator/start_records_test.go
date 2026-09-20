package orchestrator_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type memoizedProbePipe struct{ sparkwing.Base }

func (memoizedProbePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "probe", func(_ context.Context) error { return nil })
	node := plan.Job("probe")
	if node == nil {
		return errors.New("the probe job left the plan, so this fixture reaches no cache-hit path")
	}
	node.Memoize(func(_ context.Context) (sparkwing.CacheKey, error) {
		return sparkwing.Key("start-records", "static"), nil
	})
	return nil
}

func init() {
	register("orch-start-records-memoized", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &memoizedProbePipe{} })
}

type envelopeRecord struct {
	Event string         `json:"event"`
	Attrs map[string]any `json:"attrs"`
}

// safety: keeps every record of an event, not the last. Last-write-wins would
// pass a run where one node_start of several carries no reading.
func recordsByEvent(t *testing.T, envelope []byte) map[string][]envelopeRecord {
	t.Helper()
	seen := map[string][]envelopeRecord{}
	sc := bufio.NewScanner(bytes.NewReader(envelope))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var rec envelopeRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		seen[rec.Event] = append(seen[rec.Event], rec)
	}
	return seen
}

func runOnce(t *testing.T, p orchestrator.Paths, pipeline string) map[string][]envelopeRecord {
	t.Helper()
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: pipeline})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	envelope, err := os.ReadFile(p.EnvelopeLog(res.RunID))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	return recordsByEvent(t, envelope)
}

func assertVolume(t *testing.T, event string, attrs map[string]any) {
	t.Helper()
	free, ok := attrs["disk_free_bytes"].(float64)
	if !ok {
		t.Errorf("%s carries no disk_free_bytes, so a reader has no disk figure beside the run's own numbers", event)
		return
	}
	total, ok := attrs["disk_total_bytes"].(float64)
	if !ok {
		t.Errorf("%s carries free bytes with no total, so a reader cannot tell how close the volume is to full", event)
		return
	}
	// safety: a full volume truthfully reports zero free, so the reading is
	// bounded below by zero rather than by one.
	if total <= 0 || free < 0 || free >= total {
		t.Errorf("%s reports %v free of %v total, which describes no real volume", event, free, total)
	}
}

func TestRun_StartRecordsMeasureTheRunsOwnVolume(t *testing.T) {
	p := newPaths(t)
	byEvent := runOnce(t, p, "orch-plan-time-log")

	for _, event := range []string{"run_start", "node_start"} {
		recs := byEvent[event]
		if len(recs) == 0 {
			t.Errorf("the envelope carries no %s record, so this run says nothing about disk reporting", event)
			continue
		}
		for _, rec := range recs {
			assertVolume(t, event, rec.Attrs)
		}
	}

	if got, _ := byEvent["run_start"][0].Attrs["disk_path"].(string); got != p.Root {
		t.Errorf("run_start measured %q; want the run's own root %q", got, p.Root)
	}
	if got, _ := byEvent["node_start"][0].Attrs["disk_path"].(string); got != p.Root {
		t.Errorf("node_start measured %q; want the run's own root %q", got, p.Root)
	}
}

func TestRun_CachedNodeStartCarriesTheDiskReading(t *testing.T) {
	p := newPaths(t)
	runOnce(t, p, "orch-start-records-memoized")
	byEvent := runOnce(t, p, "orch-start-records-memoized")

	if got, _ := byEvent["node_end"][0].Attrs["outcome"].(string); got != "cached" {
		t.Fatalf("second run's node outcome = %q, want cached; this test no longer reaches the cached dispatch path", got)
	}
	assertVolume(t, "node_start", byEvent["node_start"][0].Attrs)
	if cacheHit, _ := byEvent["node_start"][0].Attrs["cache_hit"].(bool); !cacheHit {
		t.Error("cached node_start lost its cache_hit attr when the disk reading was merged in")
	}
}
