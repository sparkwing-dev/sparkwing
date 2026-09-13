package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func memoryRows() (measured, unmeasured wingwire.QueueState) {
	row := wingwire.ResourceState{
		Key:       "memory",
		Capacity:  17179869184,
		Held:      0,
		Reserved:  3435973836,
		External:  0,
		Available: 13743895348,
	}
	measuredRow, unmeasuredRow := row, row
	measuredRow.ExternalSource = wingwire.ExternalMeasured
	unmeasuredRow.ExternalSource = wingwire.ExternalUnmeasured
	return wingwire.QueueState{Resources: []wingwire.ResourceState{measuredRow}},
		wingwire.QueueState{Resources: []wingwire.ResourceState{unmeasuredRow}}
}

func TestRenderQueuePretty_UnmeasuredExternalPrintsTheWordNotAFigure(t *testing.T) {
	measured, unmeasured := memoryRows()

	var mb, ub bytes.Buffer
	if err := opsview.RenderQueuePretty(&mb, measured); err != nil {
		t.Fatalf("render measured: %v", err)
	}
	if err := opsview.RenderQueuePretty(&ub, unmeasured); err != nil {
		t.Fatalf("render unmeasured: %v", err)
	}
	measuredOut, unmeasuredOut := mb.String(), ub.String()

	blindRow := resourceRowLine(t, unmeasuredOut, "memory")
	if !strings.Contains(blindRow, "unmeasured") {
		t.Fatalf("memory row %q carries a figure for a dimension nobody read", blindRow)
	}
	liveRow := resourceRowLine(t, measuredOut, "memory")
	if strings.Contains(liveRow, "unmeasured") {
		t.Fatalf("measured memory row %q labeled unmeasured", liveRow)
	}
	wantNote := "external: unmeasured on memory (host sensor unavailable); no external load subtracted from available"
	if !strings.Contains(unmeasuredOut, wantNote) {
		t.Fatalf("missing %q in:\n%s", wantNote, unmeasuredOut)
	}
	if strings.Contains(measuredOut, wantNote) {
		t.Fatalf("measured rendering carries the unmeasured note:\n%s", measuredOut)
	}
	if !strings.Contains(liveRow, "0 B") {
		t.Fatalf("measured memory external rendered without a byte figure: %q", liveRow)
	}
}

func resourceRowLine(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, key+" ") {
			return line
		}
	}
	t.Fatalf("no %q resource row in:\n%s", key, out)
	return ""
}

func TestRenderQueuePlain_UnmeasuredExternalIsAddressable(t *testing.T) {
	measured, unmeasured := memoryRows()

	var mb, ub bytes.Buffer
	if err := opsview.RenderQueue(&mb, measured, "plain"); err != nil {
		t.Fatalf("render measured: %v", err)
	}
	if err := opsview.RenderQueue(&ub, unmeasured, "plain"); err != nil {
		t.Fatalf("render unmeasured: %v", err)
	}
	if want := "external\tunmeasured\tmemory\n"; !strings.Contains(ub.String(), want) {
		t.Fatalf("missing %q in plain output:\n%s", want, ub.String())
	}
	if strings.Contains(mb.String(), "unmeasured") {
		t.Fatalf("measured plain output claims unmeasured:\n%s", mb.String())
	}
}

func TestRenderQueueJSON_CarriesExternalSource(t *testing.T) {
	_, unmeasured := memoryRows()
	var buf bytes.Buffer
	if err := opsview.RenderQueue(&buf, unmeasured, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var got wingwire.QueueState
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(got.Resources) != 1 || got.Resources[0].ExternalSource != wingwire.ExternalUnmeasured {
		t.Fatalf("external source lost on the wire: %+v", got.Resources)
	}
}

func TestExternalAgeNote_ShowsHowOldTheReadingIs(t *testing.T) {
	qs := wingwire.QueueState{
		Resources:           []wingwire.ResourceState{{Key: "cores", Capacity: 8, Available: 6.4, ExternalSource: wingwire.ExternalMeasured}},
		ExternalSampleAgeMS: 45000,
	}
	if got, want := opsview.ExternalAgeNote(qs), "external reading: 45s old"; got != want {
		t.Fatalf("age note = %q, want %q", got, want)
	}
	if got := opsview.ExternalAgeNote(wingwire.QueueState{}); got != "" {
		t.Fatalf("age note = %q for a daemon that reports no age, want empty", got)
	}
}

func TestExternalAgeNote_DistinguishesMeasurementFromEffectiveValue(t *testing.T) {
	qs := wingwire.QueueState{
		ExternalSampleAgeMS:      45000,
		ExternalMeasurementAgeMS: 3000,
	}
	if got, want := opsview.ExternalAgeNote(qs), "external reading: 45s old (host sampled 3s ago)"; got != want {
		t.Fatalf("ExternalAgeNote = %q, want %q", got, want)
	}
}

func coresUnattributed(a *wingwire.ExternalAttribution) wingwire.QueueState {
	return wingwire.QueueState{
		Resources: []wingwire.ResourceState{
			{Key: "cores", ExternalSource: wingwire.ExternalUnattributed},
		},
		ExternalAttribution: a,
	}
}

func TestExternalAttributionNote_LeadsWithWhatTheReadingCarries(t *testing.T) {
	qs := coresUnattributed(&wingwire.ExternalAttribution{
		Samples: 120, SamplerUnreadable: 7, RunsAwaitingMeasure: 3,
	})
	want := "external attribution: recent readings carry some of this daemon's own runs' CPU," +
		" so external reads high and available reads low by it" +
		" (since this daemon started: the process sampler read nothing on 7 of 120 readings" +
		"; a holding run had no CPU figure yet on 3 of 120)"
	if got := opsview.ExternalAttributionNote(qs); got != want {
		t.Fatalf("attribution note = %q, want %q", got, want)
	}
}

func TestExternalAttributionNote_DropsTheCauseThatDidNotHappen(t *testing.T) {
	qs := coresUnattributed(&wingwire.ExternalAttribution{Samples: 40, RunsWithoutProcess: 2})
	want := "external attribution: recent readings carry some of this daemon's own runs' CPU," +
		" so external reads high and available reads low by it" +
		" (since this daemon started: a holding run reported no process id on 2 of 40)"
	if got := opsview.ExternalAttributionNote(qs); got != want {
		t.Fatalf("attribution note = %q, want %q: a cause that did not happen must not be reported as zero", got, want)
	}
}

func TestExternalAttributionNote_IsSilentOnceTheFigureIsClean(t *testing.T) {
	if got := opsview.ExternalAttributionNote(wingwire.QueueState{}); got != "" {
		t.Fatalf("attribution note = %q for a daemon that predates the field, want empty", got)
	}
	clean := wingwire.QueueState{
		Resources:           []wingwire.ResourceState{{Key: "cores", ExternalSource: wingwire.ExternalMeasured}},
		ExternalAttribution: &wingwire.ExternalAttribution{Samples: 900, SamplerUnreadable: 1},
	}
	if got := opsview.ExternalAttributionNote(clean); got != "" {
		t.Fatalf("attribution note = %q, want empty: one bad reading at boot must not print on every queue for the daemon's life", got)
	}
}

func TestExternalAttributionNote_IsSilentBeforeAnyReading(t *testing.T) {
	qs := coresUnattributed(&wingwire.ExternalAttribution{})
	if got := opsview.ExternalAttributionNote(qs); got != "" {
		t.Fatalf("attribution note = %q, want empty: a daemon that has read nothing yet has no reading to describe", got)
	}
}

func TestExternalAttributionNote_IsSilentWhenExternalIsIgnored(t *testing.T) {
	qs := coresUnattributed(&wingwire.ExternalAttribution{Samples: 40, RunsWithoutProcess: 2})
	qs.IgnoreExternal = true
	if got := opsview.ExternalAttributionNote(qs); got != "" {
		t.Fatalf("attribution note = %q, want empty: admission subtracts no external load at all here, so nothing reads low by it", got)
	}
}

func TestRenderQueuePlain_CarriesTheAttributionCountsWhenClean(t *testing.T) {
	qs := wingwire.QueueState{
		ExternalAttribution: &wingwire.ExternalAttribution{Samples: 900},
	}
	var out strings.Builder
	if err := opsview.RenderQueue(&out, qs, "plain"); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"external-attribution-samples\t900\n",
		"external-attribution-attributed\t0\n",
		"external-attribution-sampler-unreadable\t0\n",
		"external-attribution-runs-without-process\t0\n",
		"external-attribution-runs-process-gone\t0\n",
		"external-attribution-runs-awaiting-measure\t0\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain output = %q, want the row %q: a machine reader needs the denominator even when nothing went wrong, and one row per count so a later count is additive",
				out.String(), want)
		}
	}
}
