package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (f *storeHostFixture) report(ctx context.Context, input NodeInput, outcome store.NodeState) error {
	actor := store.Actor{Kind: store.ActorHolder, Holder: input.Authority.Holder, CapabilityPrefix: input.Authority.CapabilityPrefix, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix}, Generation: runspec.ClaimGeneration(int64(input.Generation))}
	started, err := f.State.TransitionNode(ctx, store.StartNode{Ref: controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}, Actor: actor, SourceAttempt: new(input.Attempt)})
	if err != nil {
		return err
	}
	_, err = f.State.TransitionNode(ctx, store.ReportNode{Ref: controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}, Actor: actor, SourceAttempt: new(started.Attempt), Outcome: outcome, Result: &store.NodeResult{Outcome: outcome}})
	return err
}

func TestHostedRunEmitsTheDispatchersEvents(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "ok", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "boom", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "after-boom", Needs: []string{"boom"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "skipper", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "superseded", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		outcome := store.NodeSuccess
		switch input.NodeID {
		case "boom":
			outcome = store.NodeFailed
		case "skipper":
			outcome = store.NodeSkipped
		case "superseded":
			outcome = store.NodeSuperseded
		}
		return f.report(ctx, input, outcome)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "failed" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	events, err := f.Tenant.ListEventsAfter(t.Context(), f.Accepted.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for id, outcome := range map[string]string{"ok": "succeeded", "boom": "failed", "after-boom": "cancelled", "skipper": "skipped", "superseded": "superseded"} {
		starts, ends := 0, 0
		for _, event := range events {
			if event.NodeID != id {
				continue
			}
			if event.Kind == "job_started" {
				starts++
			}
			if event.Kind == "job_"+outcome {
				ends++
			}
		}
		wantStarts := 1
		if id == "after-boom" {
			wantStarts = 0
		}
		if starts != wantStarts || ends != 1 {
			t.Errorf("%s starts=%d ends=%d, want %d and 1", id, starts, ends, wantStarts)
		}
	}
}
