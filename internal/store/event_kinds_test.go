package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRunEventKindsRegisterEveryStateEvent(t *testing.T) {
	for _, rule := range nodeEvents() {
		if !slices.Contains(runstate.RunEventKinds, rule.Kind) {
			t.Errorf("job event %q is not in runstate.RunEventKinds", rule.Kind)
		}
	}
	for _, state := range []runstate.RunState{runstate.RunPending, runstate.RunSuccess, runstate.RunFailed, runstate.RunCancelled} {
		if !slices.Contains(runstate.RunEventKinds, "run_"+string(state)) {
			t.Errorf("run event run_%s is not in runstate.RunEventKinds", state)
		}
	}
	s, tenant, _, _ := resultFixture(t)
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		return appendStateEventTx(t.Context(), tx, tenant.team, runspec.NodeRef{RunID: "run"}, "unlisted_state", 1, nil, transactionTime(t, tx))
	})
	if !errors.Is(err, errInvalidEventKind) {
		t.Fatalf("a state event outside RunEventKinds was appended: %v", err)
	}
}
