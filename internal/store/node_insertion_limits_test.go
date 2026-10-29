package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func planWithJobs(t *testing.T, tenant *Tenant, request PlanAcceptance, count int) PlanAcceptance {
	t.Helper()
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
	for i := 0; i < count; i++ {
		plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: fmt.Sprintf("job-%d", i)})
	}
	var err error
	request.Seal.Plan, err = json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: request.Seal.Declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestPlanInsertionLimitsAreAtomicProperty(t *testing.T) {
	property := func(rawCap, rawCount uint8, metered, unrelated bool) bool {
		tenant, request, _ := planFixture(t, runspec.DispatchHost)
		cap, count := int64(rawCap%5), int(rawCount%7)
		if metered {
			p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), request.Actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			c := p.Capability
			if unrelated {
				admitted, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: request.Seal.Request.AuthorityRef, Request: request.Seal.Request.Request})
				if err != nil {
					t.Fatal(err)
				}
				c.Prefix = "unrelated-planner"
				c.Digest = authorityRoot(c.Prefix, "a").Digest
				c.Binding.RunID, c.UnitID = admitted.RunID, admitted.RunID
				c.Billing.ReservationID = "award:" + c.Prefix
			} else {
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix='planner'`)
			}
			c.Billing.Metered = true
			c.Billing.PayerTeam = c.Team
			authorityPersist(t, tenant.s, c)
		}
		if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{ComputeLimitNodesPerRun: cap}); err != nil {
			t.Fatal(err)
		}
		request = planWithJobs(t, tenant, request, count)
		_, err := tenant.AcceptPlan(t.Context(), request)
		refused := cap > 0 && int64(count) > cap && metered && !unrelated
		if refused {
			var limit *ComputeLimitError
			if !errors.As(err, &limit) || limit.Cap != cap || limit.Observed != cap || limit.Limit != ComputeLimitNodesPerRun {
				t.Logf("limit=%+v err=%v", limit, err)
				return false
			}
		} else if err != nil {
			t.Log(err)
			return false
		}
		snapshot, err := tenant.ReadSnapshot(t.Context(), request.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if refused {
			return snapshot.Run.Spec == nil && len(snapshot.Nodes) == 0
		}
		if len(snapshot.Nodes) != count || snapshot.Run.Spec == nil {
			return false
		}
		if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 1}); err != nil {
			t.Fatal(err)
		}
		replay, err := tenant.AcceptPlan(t.Context(), request)
		return err == nil && replay.Replayed
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 48})); err != nil {
		t.Fatal(err)
	}
}

func TestPlanInsertionGlobalLimitAndReplay(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	request = planWithJobs(t, tenant, request, 3)
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); !errors.Is(err, ErrComputeLimit) {
		t.Fatalf("oversized plan: %v", err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("accepted replay: %+v %v", replay, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 3)
}

func TestExpansionInsertionLimitRollsBackBatch(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	request.Body = []byte(`{"jobs":[{"id":"one","steps":[{"id":"step"}],"groups":["group"],"deps":["job"]},{"id":"two","steps":[{"id":"step"}],"groups":["group"],"deps":["job"]}]}`)
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptExpansion(t.Context(), request); !errors.Is(err, ErrComputeLimit) {
		t.Fatalf("oversized expansion: %v", err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 1)
	source, err := tenant.ReadNode(t.Context(), request.Source)
	if err != nil || source.Expansion != runstate.ExpansionExpected {
		t.Fatalf("refused expansion changed source: %+v %v", source, err)
	}
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("accepted expansion replay: %+v %v", replay, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 3)
}

func TestOwnerReadyReplayPreservesQueueAge(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchHost)
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}
	change := nodeTransition{Ref: ref, Actor: request.Actor, To: runstate.NodeReady, Cause: runstate.CauseRelease}
	first, err := tenant.TransitionNode(t.Context(), change)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || row.ReadyAt == nil || !row.ReadyAt.Equal(*now) {
		t.Fatalf("ready facts: %+v %v", row, err)
	}
	*now = now.Add(1)
	replay, err := tenant.TransitionNode(t.Context(), change)
	if err != nil || !replay.Replayed || replay.StateSeq != first.StateSeq {
		t.Fatalf("ready replay: %+v %v", replay, err)
	}
	after, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || after.ReadyAt == nil || !after.ReadyAt.Equal(*row.ReadyAt) || after.StateSeq != row.StateSeq {
		t.Fatalf("replay changed queue: %+v %v", after, err)
	}
}

func TestConcurrentPlanAcceptanceCannotExceedInsertionLimit(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	request = planWithJobs(t, tenant, request, 3)
	for _, cap := range []int64{2, 3} {
		if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{computeLimitGlobalNodesPerRun: cap}); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 8)
		for range 8 {
			go func() { _, err := tenant.AcceptPlan(t.Context(), request); results <- err }()
		}
		for range 8 {
			err := <-results
			if cap == 2 && !errors.Is(err, ErrComputeLimit) || cap == 3 && err != nil {
				t.Fatalf("cap %d: %v", cap, err)
			}
		}
		want := 0
		if cap == 3 {
			want = 3
		}
		rewriteSchemaCount(t, tenant.s.db, "nodes", want)
	}
}
