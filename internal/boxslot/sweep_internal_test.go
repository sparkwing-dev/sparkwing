package boxslot

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// Pins envelopeFileName to the orchestrator's paths layout: the sweep
// stats <runsDir>/<runID>/_envelope.ndjson, and this assertion goes
// red if the paths package ever moves the envelope.
func TestEnvelopePathMatchesPathsLayout(t *testing.T) {
	p := paths.PathsAt("/root")

	got := filepath.Join(p.RunsDir(), "run-x", envelopeFileName)

	if want := p.EnvelopeLog("run-x"); got != want {
		t.Fatalf("sweep envelope path %q != paths.EnvelopeLog %q", got, want)
	}
}
