package wingwire

import (
	"fmt"
	"testing"
	"testing/quick"
)

func TestHolderReferencesKeepRunIdentity(t *testing.T) {
	if err := quick.Check(func(number uint64, node string) bool {
		run := fmt.Sprintf("run-%x", number)
		for _, ref := range []HolderRef{HolderRef(run), NodeHostParticipant(run, node), NodeSemaphoreParticipant(run, node), PlanSemaphoresParticipant(run)} {
			if ref.RunID() != RunID(run) {
				return false
			}
		}
		return true
	}, nil); err != nil {
		t.Fatal(err)
	}
}
