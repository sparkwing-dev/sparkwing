package nodehost

import (
	"context"
	"errors"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"log/slog"
	"testing"
	"time"
)

func successfulExecution(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} }

func TestResumeRefusesARowPlannedUnderAnotherSpec(t *testing.T) {
	st := openStore(t)
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.Node{
		{RunID: testRunID, NodeID: "held", Status: "pending", SpecHash: jobHash(nodeproto.JobSpec{ID: "held", Steps: []nodeproto.StepSpec{{ID: "body"}}})},
		{RunID: testRunID, NodeID: "lost", Status: "pending", SpecHash: jobHash(nodeproto.JobSpec{ID: "lost", Steps: []nodeproto.StepSpec{{ID: "body"}}})},
		{RunID: testRunID, NodeID: "build", Status: "pending", SpecHash: jobHash(nodeproto.JobSpec{ID: "build", Optional: true, Steps: []nodeproto.StepSpec{{ID: "body"}}})},
	} {
		if err := st.CreateNode(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ClaimNamedNode(ctx, store.ClaimIdentity{}, testRunID, "held", "pool:live", time.Hour, store.NamedClaimOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "lost", "failed", "holder lost", nil, store.FailureAgentLost, nil); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	s := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}, resume: true}
	err := s.add(ctx, ctx, []nodeproto.JobSpec{
		{ID: "held", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "lost", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}, nil)
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.PlanDrift {
		t.Fatalf("add = %v, want plan_drift", err)
	}
	if row := nodeRow(t, st, "held"); row.Status == "done" || row.ClaimedBy != "pool:live" {
		t.Errorf("held = %s/%s claimed by %q, want left to its live holder", row.Status, row.Outcome, row.ClaimedBy)
	}
	if row := nodeRow(t, st, "lost"); row.FailureReason != store.FailureAgentLost {
		t.Errorf("lost = %s/%s (%s), want its agent_lost record kept", row.Status, row.Outcome, row.FailureReason)
	}
}
