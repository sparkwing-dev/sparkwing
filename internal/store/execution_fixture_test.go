package store

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func admitExecution(t *testing.T, tenant *Tenant, actor runstate.Actor) {
	t.Helper()
	plan := []byte(fmt.Sprintf(`{"jobs":[{"id":%q}]}`, actor.NodeID))
	if _, err := tenant.s.exec(t.Context(), `UPDATE runs SET spec_json=?,spec_hash='fixture',plan_json=?,plan_hash='fixture',sealed_at=1 WHERE team=? AND id=? AND spec_json IS NULL`, []byte(`{}`), plan, tenant.team, actor.RunID); err != nil {
		t.Fatal(err)
	}
	binding := runstate.CapacityBinding{RunID: string(actor.RunID), NodeID: string(actor.NodeID), Generation: int64(actor.Generation)}
	if result, err := tenant.MayStart(t.Context(), binding, actor); err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("execution admission: %+v, %v", result, err)
	}
}
