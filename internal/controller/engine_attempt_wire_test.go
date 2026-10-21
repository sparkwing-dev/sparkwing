package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func FuzzEngineTransitionSourceAttemptProjection(f *testing.F) {
	f.Add(int64(0), true)
	f.Add(int64(3), true)
	f.Add(int64(-1), true)
	f.Add(int64(0), false)
	f.Fuzz(func(t *testing.T, ordinal int64, present bool) {
		input := controllerwire.JobTransitionRequest{To: "running", Cause: "start"}
		if present {
			input.SourceAttempt = &ordinal
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded controllerwire.JobTransitionRequest
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		command, err := captureEngineTransition(t.Context(), decoded)
		request, ok := command.(store.StartNode)
		if !ok {
			t.Fatalf("unexpected command %T", command)
		}
		if err != nil || (request.SourceAttempt != nil) != present {
			t.Fatalf("attempt presence changed: %+v %v", request, err)
		}
		if present && int64(*request.SourceAttempt) != ordinal {
			t.Fatal("source ordinal changed before the store boundary")
		}
	})
}

func TestEngineWireRetryOrdinalCannotConsumeNextAttempt(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	actor := engineRuntimeClaim(t, server, capability.UnitNode)
	settings := runspec.JobExecutionSettings{}
	settings.Retry.Set, settings.Retry.Value = true, 3
	execution, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, execution)
	start := controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: new(int64(0))}
	for _, ordinal := range []*int64{nil, new(int64(-1)), new(int64(1))} {
		input := start
		input.SourceAttempt = ordinal
		if _, err := server.TransitionJob(t.Context(), actor, input); err == nil {
			t.Fatal("unbound start consumed an attempt")
		}
	}
	started, err := server.TransitionJob(t.Context(), actor, start)
	if err != nil || started.Attempt != 1 {
		t.Fatalf("start: %+v %v", started, err)
	}
	retry := controllerwire.JobTransitionRequest{To: "running", Cause: "retry", SourceAttempt: &started.Attempt, Result: &controllerwire.JobResult{Outcome: "failed", Reason: "same failure"}}
	second, err := server.TransitionJob(t.Context(), actor, retry)
	if err != nil || second.Attempt != 2 {
		t.Fatalf("retry: %+v %v", second, err)
	}
	replay, err := server.TransitionJob(t.Context(), actor, retry)
	if err != nil || !replay.Replayed || replay.Attempt != second.Attempt {
		t.Fatalf("retry replay consumed an attempt: %+v %v", replay, err)
	}
	current := retry
	current.SourceAttempt = &second.Attempt
	third, err := server.TransitionJob(t.Context(), actor, current)
	if err != nil || third.Attempt != 3 || third.Replayed {
		t.Fatalf("identical failure on next attempt lost: %+v %v", third, err)
	}
	if _, err := server.TransitionJob(t.Context(), actor, retry); err == nil {
		t.Fatal("delayed retry affected a later attempt")
	}
	stale := controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: &second.Attempt, Result: &controllerwire.JobResult{Outcome: "success"}}
	if _, err := server.TransitionJob(t.Context(), actor, stale); err == nil {
		t.Fatal("delayed report finished a later attempt")
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Attempt != 3 || row.ApplicationRetries != 2 || row.Status != store.StatusRunning {
		t.Fatalf("refused write changed current execution: %+v %v", row, err)
	}
}

type transitionCapture struct {
	RunTransactions
	change store.NodeCommand
}

func (c *transitionCapture) TransitionNode(_ context.Context, change store.NodeCommand) (store.Applied, error) {
	c.change = change
	return store.Applied{}, nil
}

func captureEngineTransition(ctx context.Context, body controllerwire.JobTransitionRequest) (store.NodeCommand, error) {
	capture := &transitionCapture{}
	server := &EngineController{ResolveTenant: func(context.Context, store.Team) (TenantHandle, error) { return capture, nil }}
	_, err := server.TransitionJob(ctx, capability.Authorized{Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}, body)
	return capture.change, err
}

func (f *transitionCapture) Team() store.Team { return "a" }
