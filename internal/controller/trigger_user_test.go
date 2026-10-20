package controller_test

import (
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTrigger_IgnoresAUserNamedInTheBody(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	property := func(id, display string) bool {
		body := computeIntake("demo")
		body.Request.Actor = runspec.Actor{Kind: "operator", ID: id, Display: display}
		created := floodRun(t, f.intake(f.token, body), 201)
		row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).ReadRun(t.Context(), created.RunID)
		if err != nil {
			t.Fatal(err)
		}
		return row.Request.Request.Actor == (runspec.Actor{Kind: "account", ID: "alice"})
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
