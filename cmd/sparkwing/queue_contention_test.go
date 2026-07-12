package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestRenderQueuePretty_ContendedMarkerAndExplanation(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{{
			RunID:            "run-1",
			Pipeline:         "work",
			Contended:        true,
			ContentionReason: "elapsed 12m0s past p99 8m30s; host saturated 62% of the run",
		}},
		Events: &wingwire.EventsWindow{WindowMS: 86400000, Runs: 5, Contended: 2},
	}
	var buf bytes.Buffer
	if err := renderQueuePretty(&buf, qs); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "(contended)") {
		t.Error("holder row missing the (contended) marker")
	}
	if !strings.Contains(out, "host saturated 62% of the run") {
		t.Error("missing the contention explanation line")
	}
	if !strings.Contains(out, "2 contended") {
		t.Errorf("events line missing the contended count; got:\n%s", out)
	}
}

func TestFmtEventsLine_OmitsContendedWhenZero(t *testing.T) {
	line := fmtEventsLine(&wingwire.EventsWindow{WindowMS: 86400000, Runs: 3})
	if strings.Contains(line, "contended") {
		t.Errorf("events line should not mention contended when none occurred: %q", line)
	}
}
