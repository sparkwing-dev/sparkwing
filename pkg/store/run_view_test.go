package store

import (
	"encoding/hex"
	"errors"
	"testing"
	"testing/quick"
)

func TestRunViewNeverReturnsAnotherTenantsState(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	if view, err := tenant.ReadRunView(t.Context(), "run"); err != nil || len(view.Attempts) != 1 || view.Attempts[0].Identity.Attempt != 3 {
		t.Fatalf("own view: %+v, %v", view, err)
	}
	property := func(name string) bool {
		foreign := &Tenant{s: tenant.s, team: Team("foreign-" + hex.EncodeToString([]byte(name)))}
		view, err := foreign.ReadRunView(t.Context(), "run")
		return errors.Is(err, ErrNotFound) && view.Snapshot.Run.ID == "" && len(view.Snapshot.Nodes) == 0 && len(view.Attempts) == 0 && len(view.Logs) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestRunViewDiscardsPartialStateOnReadFailure(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `DROP TABLE log_streams`)
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err == nil || view.Snapshot.Run.ID != "" || len(view.Attempts) != 0 {
		t.Fatalf("partial view escaped a failed snapshot: %+v, %v", view, err)
	}
}
