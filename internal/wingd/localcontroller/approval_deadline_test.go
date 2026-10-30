package localcontroller

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestMaintenanceSettlesTenSecondApprovalWithinOneSecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var b Binding
		c := openFixture(t, func(_ context.Context, binding Binding) (Services, error) {
			b = binding
			return serviceFixture(binding), nil
		})
		seedOwner(t, b, "run", true)
		db := stateSQL(t, b)
		if _, err := db.ExecContext(t.Context(), `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id='run'`, time.Now().Add(time.Hour).UnixNano(), b.Tenant.Team()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES (?,'run','gate','approval_pending',?,?)`, b.Tenant.Team(), []byte(`{"id":"gate","approval":{}}`), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO approvals(team,run_id,node_id,requested_at,timeout_ms,on_timeout) VALUES (?,'run','gate',?,10000,'deny')`, b.Tenant.Team(), time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		joined := make(chan error, 1)
		go func() { joined <- c.maintain(ctx) }()
		synctest.Wait()
		time.Sleep(11 * time.Second)
		synctest.Wait()
		node, err := c.tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "gate"})
		if err != nil || node.Outcome != runstate.NodeFailed {
			t.Errorf("approval after its deadline: %+v, %v", node, err)
		}
		cancel()
		if err := <-joined; err != nil {
			t.Fatal(err)
		}
	})
}
