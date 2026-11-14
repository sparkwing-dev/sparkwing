package store_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func deliveriesOf(t *testing.T, s *store.Store, team store.Team) []teststore.RunEventDeliveryRow {
	t.Helper()
	rows, err := teststore.RunEventDeliveries(t.Context(), s, team)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func deliveryKeys(rows []teststore.RunEventDeliveryRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.SubscriptionID+" "+r.EventType+" "+r.EventID)
	}
	return out
}

func noJobs(_ *store.IntakeRequest, doc *nodewire.PlanDoc) { doc.Jobs = []nodewire.JobSpec{} }

func TestRunTransitionQueuesMatchingRunEvents(t *testing.T) {
	ctx := t.Context()
	s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequestWith(ctx, s, noJobs)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(ctx, plan.RunID)
	if err != nil {
		t.Fatal(err)
	}
	team, repo := run.Team, string(run.Request.Request.Git.Repository.Key)
	both := []string{runevents.EventStarted, runevents.EventFinished}
	finished := []string{runevents.EventFinished}
	for id, f := range map[string]runevents.Filter{
		"sub_all":       {EventTypes: both},
		"sub_started":   {EventTypes: []string{runevents.EventStarted}},
		"sub_success":   {EventTypes: finished, Statuses: []string{"success"}},
		"sub_failed":    {EventTypes: finished, Statuses: []string{"failed"}},
		"sub_pipeline":  {EventTypes: both, Pipelines: []string{run.Pipeline}},
		"sub_otherpipe": {EventTypes: both, Pipelines: []string{"not-" + run.Pipeline}},
		"sub_repo":      {EventTypes: both, Repositories: []string{repo}},
		"sub_otherrepo": {EventTypes: both, Repositories: []string{"gh:999999"}},
	} {
		if err := teststore.SeedRunEventSubscription(ctx, s, team, id, "https://hooks.example.test/"+id, "sealed", f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if rows := deliveriesOf(t, s, team); len(rows) != 0 {
		t.Fatalf("plan acceptance queued events: %v", deliveryKeys(rows))
	}
	started, err := tenant.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: runstate.RunRunning, Cause: runstate.RunStart})
	if err != nil {
		t.Fatal(err)
	}
	finish := runstate.FinishRunRequest{Unit: plan.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Cause: runstate.RunFinish, Actor: plan.Actor}}
	done, err := tenant.FinishRunAndUnit(ctx, finish)
	if err != nil || done.Outcome != runstate.RunSuccess {
		t.Fatalf("finish: %+v, %v", done, err)
	}
	if replay, err := tenant.FinishRunAndUnit(ctx, finish); err != nil || !replay.Replayed {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	startID, finishID := runevents.ID(plan.RunID, started.StateSeq), runevents.ID(plan.RunID, done.StateSeq)
	var want []string
	for _, sub := range []string{"sub_all", "sub_pipeline", "sub_repo", "sub_started"} {
		want = append(want, sub+" run.started "+startID)
	}
	for _, sub := range []string{"sub_all", "sub_pipeline", "sub_repo", "sub_success"} {
		want = append(want, sub+" run.finished "+finishID)
	}
	slices.Sort(want)
	rows := deliveriesOf(t, s, team)
	got := deliveryKeys(rows)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("deliveries:\n got %v\nwant %v", got, want)
	}
	for _, r := range rows {
		var event struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			StateSeq int64  `json:"sparkwingstateseq"`
			Data     struct {
				Status    string  `json:"status"`
				StartedAt *string `json:"started_at"`
			} `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &event); err != nil {
			t.Fatal(err)
		}
		if event.ID != r.EventID || fmt.Sprintf("%s/%d", plan.RunID, event.StateSeq) != r.EventID || r.State != "pending" || r.Attempts != 0 || r.DeadlineAt.Sub(r.NextAt) != store.RunEventDeliveryWindow || event.Data.StartedAt == nil {
			t.Fatalf("delivery %+v carries %+v", r, event)
		}
		if r.EventType == runevents.EventFinished && (event.Type != runevents.TypeFinished || event.Data.Status != "success") {
			t.Fatalf("finished event %+v", event)
		}
	}
}

func TestRunEventWriteFailureRollsBackTheTransition(t *testing.T) {
	ctx := t.Context()
	s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequestWith(ctx, s, noJobs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	before, err := tenant.ReadRun(ctx, plan.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRunEventSubscription(ctx, s, before.Team, "sub_all", "https://hooks.example.test/", "sealed", runevents.Filter{EventTypes: []string{runevents.EventStarted}}); err != nil {
		t.Fatal(err)
	}
	// safety: a delivery already holding the start event's key makes the outbox insert fail inside the transition.
	if err := teststore.SeedRunEventDelivery(ctx, s, before.Team, "sub_all", runevents.ID(plan.RunID, before.StateSeq+1), plan.RunID, []byte(`{}`), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: runstate.RunRunning, Cause: runstate.RunStart}); err == nil {
		t.Fatal("start committed although its event could not be queued")
	}
	after, err := tenant.ReadRun(ctx, plan.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != runstate.RunPending || after.StateSeq != before.StateSeq || after.StartedAt != nil {
		t.Fatalf("run moved without its event: %+v", after.Execution)
	}
	events, err := tenant.ReadStateEvents(ctx, plan.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "run_started" {
			t.Fatalf("state event survived rollback: %+v", e)
		}
	}
	if rows := deliveriesOf(t, s, before.Team); len(rows) != 1 {
		t.Fatalf("deliveries after rollback: %v", deliveryKeys(rows))
	}
}
