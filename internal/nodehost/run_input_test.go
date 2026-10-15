package nodehost

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestAwardedRunInputReachesNativeOwner(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		for _, original := range []string{"run", "original-run"} {
			t.Run(original+map[bool]string{false: "/new", true: "/resumed"}[resumed], func(t *testing.T) {
				state, host, prepared := newOwnerFixture(t, resumed)
				row := &state.snapshot.Run
				row.Owner.Name = ""
				row.Request.RunID, row.Request.Request.Team = original, "team"
				unit := controllerwire.ClaimedUnit{
					RunID: row.ID, Request: row.Request, Runner: prepared.GrantedRunner,
					Binding: capability.Binding{RunID: row.ID, Holder: row.Owner.Holder, Generation: row.Owner.Generation},
				}
				if resumed {
					row.Spec.Request = row.Request
					unit.Accepted = &controllerwire.PlanAccepted{Spec: *row.Spec, Plan: row.Plan, PlanHash: row.Spec.PlanHash}
				}
				input, err := NewRunInput(unit)
				if err != nil {
					t.Fatal(err)
				}
				completion, err := host.Run(t.Context(), input)
				if err != nil || completion.Outcome != "success" || state.finished.Unit != *row.Owner {
					t.Fatalf("completion=%+v finish=%+v error=%v", completion, state.finished, err)
				}
				if resumed && state.accepted.RunID != "" {
					t.Fatal("resume replaced its accepted seal")
				}
			})
		}
	}
}
