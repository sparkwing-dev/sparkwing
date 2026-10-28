package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// The console refreshes a run page on each named event of the run stream, and a browser EventSource hears only the
// names it listens for, so a kind the store starts emitting must be added to RUN_EVENT_KINDS in useRunEvents.ts.
func TestConsoleListensForEveryRunEventKind(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "useRunEvents.ts"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{"run_started", "run_cancel_requested", "job_expansion_ready", "job_approval_ready"}
	for _, state := range []runstate.RunState{runstate.RunSuccess, runstate.RunFailed, runstate.RunCancelled} {
		kinds = append(kinds, "run_"+string(state))
	}
	for _, rule := range nodeEvents() {
		kinds = append(kinds, rule.Kind)
	}
	for _, kind := range kinds {
		if !strings.Contains(string(source), strconv.Quote(kind)) {
			t.Errorf("web/src/lib/useRunEvents.ts does not listen for run event %q", kind)
		}
	}
}
