package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// memoryRows builds the same memory row twice: once as a measured reading of
// an idle machine, once as a dimension the host sampler could not read. The
// figures are identical, which is the whole problem -- an unread sensor and a
// machine with no external load produce the same numbers, so only the label
// can tell them apart.
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

// TestRenderQueuePretty_UnmeasuredExternalPrintsTheWordNotAFigure is the
// negative control for a blind sensor. The EXTERNAL cell for a dimension
// nobody read must carry the word "unmeasured", never a byte count, and the
// view must state that nothing was subtracted. Asserting only that the two
// renderings differ would pass vacuously, so this pins the literal each one
// prints, and it pins the row rather than the page: a note saying the sensor
// is blind while the table still shows a figure leaves the figure there to be
// read and believed.
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

// resourceRowLine pulls one resource table row out of a pretty rendering, so
// an assertion lands on the cell a reader sees rather than anywhere in the
// page.
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

// TestRenderQueuePlain_UnmeasuredExternalIsAddressable pins the machine-facing
// rendering, so a script reading plain output can tell a blind sensor from a
// quiet machine without parsing prose.
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

// TestRenderQueueJSON_CarriesExternalSource keeps the label on the wire, so
// the JSON consumers that drove a wrong capacity decision off this figure can
// see which numbers are readings.
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

// TestExternalAgeNote_ShowsHowOldTheReadingIs covers the other half of "this
// number is not what it appears": a reading held in force by the deadband is
// indistinguishable from a live one until it says how old it is.
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
