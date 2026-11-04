package runstate_test

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

// The check sits here, not in sessionledger, because a pipeline imports
// sessionledger and `go mod tidy` would download this package's dependencies
// for every new pipeline.
func TestStraySessionEventKindIsRegistered(t *testing.T) {
	if !slices.Contains(runstate.RunEventKinds, sessionledger.EventKind) {
		t.Fatalf("%q is not in runstate.RunEventKinds, so the console never hears it", sessionledger.EventKind)
	}
}
