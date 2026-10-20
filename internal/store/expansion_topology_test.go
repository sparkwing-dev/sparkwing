package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestExpansionValidationReceivesAcceptedPlanConcurrency(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	group := nodeproto.ConcurrencySpec{Group: "deploy", Capacity: new(3), Cost: new(1), Scope: new("global"), OnLimit: new("queue")}
	plan := nodeproto.PlanDoc{Concurrency: []nodeproto.ConcurrencySpec{group}, Jobs: []nodeproto.JobSpec{{ID: "job", Closures: nodeproto.ClosureSpec{Generate: []string{"group"}}}}}
	body, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET plan_json=? WHERE id='run'`, body)
	valid := request.Body
	request.Body = []byte(`{"jobs":[{"id":"generated","steps":[{"id":"step"}],"groups":["group"],"concurrency":[{"group":"deploy","capacity":2}]}]}`)
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("generated job replaced the accepted concurrency capacity")
	}
	request.Body = valid
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if replay, err := tenant.AcceptExpansion(t.Context(), request); err != nil || !replay.Replayed {
		t.Fatalf("expansion replay: %+v, %v", replay, err)
	}
}

func TestGeneratedDependenciesUseAcceptedTopologyProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(47, 153))
	for trial := range 32 {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			tenant, request := successfulExpansionFixture(t)
			count := random.IntN(7)
			jobs := make([]nodeproto.JobSpec, 0, count)
			for i := range count {
				jobs = append(jobs, nodeproto.JobSpec{ID: fmt.Sprintf("child-%d", i), Groups: []string{"group"}, Needs: []string{"job"}})
			}
			body, err := json.Marshal(testBodyPlan(nodeproto.PlanDoc{Jobs: jobs}))
			if err != nil {
				t.Fatal(err)
			}
			request.Body = body
			if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			snapshot, err := tenant.ReadSnapshot(t.Context(), request.Source.RunID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := snapshot.Scheduling()
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range snapshot.Nodes {
				deps := []string{"missing", row.Ref.NodeID, fmt.Sprintf("forged-%x", random.Uint64())}
				body, err := json.Marshal(deps)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET deps_json=? WHERE team=? AND run_id=? AND node_id=?`, body, tenant.team, row.Ref.RunID, row.Ref.NodeID)
			}
			snapshot, err = tenant.ReadSnapshot(t.Context(), request.Source.RunID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := snapshot.Scheduling()
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(Decide(before), Decide(after)) {
				t.Fatalf("mutable dependency projection changed accepted topology: %+v, %v", after, err)
			}
			if replay, err := tenant.AcceptExpansion(t.Context(), request); err != nil || !replay.Replayed || len(replay.Nodes) != count {
				t.Fatalf("replay changed accepted topology: %+v, %v", replay, err)
			}
		})
	}
}
