package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestControllerCancelUnclaimableJobFinishesRun(t *testing.T) {
	s, options, _, _ := retryActionDispatchFixture(t, "value", true, runspec.DispatchController)
	control, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := control.Retry(t.Context(), "run", controllerwire.RetryRequest{Mode: controllerwire.RetryAll, IdempotencyKey: "unclaimable"})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id=?`, []byte(`{"requires":{"set":true,"value":["matrix-label=absent"]}}`), created.RunID)
	s.reconcileOwnedRuns(t.Context())
	tenant := teststore.ForTeam(t, s.store, "a")
	ref := runspec.NodeRef{RunID: runspec.RunID(created.RunID), NodeID: "job"}
	job, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || job.ReadyAt == nil || job.Attempt != 0 {
		t.Fatalf("unclaimed job: %+v %v", job, err)
	}
	engineHTTPRoot(t, s.engine, "a", "worker", []capability.Grant{capability.NodesClaim})
	if claim, err := s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "worker", Kinds: []capability.UnitKind{capability.UnitNode}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unavailable label awarded: %+v %v", claim, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := control.Cancel(ctx, created.RunID, controllerwire.CancelRequest{Reason: "cancel_requested"}); err != nil {
		t.Fatal(err)
	}
	run, err := control.ReadRun(ctx, created.RunID)
	if err != nil || run.Status != "cancelled" || run.FinishedAt == nil {
		t.Fatalf("cancelled run: %+v %v", run, err)
	}
	job, err = tenant.ReadNode(ctx, ref)
	if err != nil || job.Outcome != runstate.NodeCancelled || job.Attempt != 0 {
		t.Fatalf("cancelled job: %+v %v", job, err)
	}
}
