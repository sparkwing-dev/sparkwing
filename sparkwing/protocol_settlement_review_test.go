package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type predicateDependencyJob struct {
	Base
	entered chan struct{}
	seen    int
}

func (j *predicateDependencyJob) Work(w *Work) (*WorkStep, error) {
	source := Step(w, "source", func(context.Context) (int, error) { return 42, nil }).SkipIf(func(context.Context) bool { return true })
	Step(w, "consumer", func(context.Context) error { return nil }).Needs(source).SkipIf(func(ctx context.Context) bool {
		close(j.entered)
		j.seen = StepGet[int](ctx, source)
		return j.seen == 0
	})
	return nil, nil
}

func settlementRequest(t *testing.T, raw string) nodeproto.Request {
	t.Helper()
	var request nodeproto.Request
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestProtocolPredicateObservesSkippedPredecessor(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "retry"}[retry], func(t *testing.T) {
			plan := NewPlan()
			job := &predicateDependencyJob{entered: make(chan struct{})}
			Job(plan, "job", job)
			attempt := 1
			if retry {
				result, err := (sdkOperations{}).RunStep(t.Context(), plan, settlementRequest(t, `{"job":"job","step":"source","attempt":1,"settled":[]}`))
				if err != nil || result.Error != nil || string(result.Output) != "42" {
					t.Fatalf("prior attempt: %+v, %v", result, err)
				}
				attempt = 2
			}
			request := settlementRequest(t, `{"job":"job","closure":"job/step/consumer/skip/0","settled":["source"]}`)
			request.Attempt = attempt
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				select {
				case <-job.entered:
					cancel()
				case <-ctx.Done():
				}
			}()
			result, err := (sdkOperations{}).Eval(ctx, plan, request)
			cancel()
			<-joined
			if err != nil || result.Error != nil || string(result.Value) != "true" || job.seen != 0 {
				t.Fatalf("skipped predecessor in predicate: %+v, %v, observed=%d", result, err, job.seen)
			}
		})
	}
}

func TestProtocolRejectsIncompleteSettlementBeforeResettingOutputs(t *testing.T) {
	for _, settled := range [][]string{nil, {}, {"absent"}, {"source", "source"}, {"source", "result"}} {
		plan := NewPlan()
		Job(plan, "job", &attemptJob{})
		ops := sdkOperations{}
		prior, err := ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: 1, Settled: []string{}})
		if err != nil || prior.Error != nil || string(prior.Output) != "1" {
			t.Fatalf("prior output: %+v, %v", prior, err)
		}
		_, err = ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "result", Attempt: 2, Settled: settled})
		if err == nil {
			t.Fatalf("invalid settlement %v was accepted", settled)
		}
		result, err := ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "result", Attempt: 1, Settled: []string{"source"}})
		if err != nil || result.Error != nil || string(result.Output) != "1" {
			t.Fatalf("rejected settlement reset prior state: %+v, %v", result, err)
		}
	}
}

func TestProtocolAttemptSettlementNeverReusesEarlierOutput(t *testing.T) {
	property := func(ordinal uint8) bool {
		plan := NewPlan()
		Job(plan, "job", &attemptJob{})
		ops := sdkOperations{}
		prior, err := ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: int(ordinal) + 1, Settled: []string{}})
		if err != nil || prior.Error != nil || string(prior.Output) != "1" {
			return false
		}
		result, err := ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "result", Attempt: int(ordinal) + 2, Settled: []string{"source"}})
		return err == nil && result.Error == nil && string(result.Output) == "0"
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSettlementCannotFinishActiveStep(t *testing.T) {
	plan := NewPlan()
	started, release := make(chan struct{}), make(chan struct{})
	Job(plan, "job", &parallelAttemptJob{started: started, release: release})
	finished := make(chan error, 1)
	go func() {
		result, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "wait", Attempt: 1, Settled: []string{}})
		if err == nil && result.Error != nil {
			err = result.Error
		}
		finished <- err
	}()
	<-started
	for _, request := range []nodeproto.Request{
		{Job: "job", Step: "run", Attempt: 1, Settled: []string{"wait"}},
		{Job: "job", Step: "wait", Attempt: 1, Settled: []string{}},
	} {
		_, err := (sdkOperations{}).RunStep(t.Context(), plan, request)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
			t.Errorf("request overlapped active state: %v", err)
		}
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSettlementRequiresTransitivePredecessors(t *testing.T) {
	plan := NewPlan()
	job := Job(plan, "job", &attemptJob{})
	entered := false
	Step(job.work, "leaf", func(context.Context) error { return nil }).Needs(job.work.byID["result"]).SkipIf(func(context.Context) bool {
		entered = true
		return false
	})
	_, err := (sdkOperations{}).Eval(t.Context(), plan, nodeproto.Request{Job: "job", Closure: "job/step/leaf/skip/0", Attempt: 1, Settled: []string{"result"}})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || entered {
		t.Fatalf("incomplete transitive settlement entered predicate=%v: %v", entered, err)
	}
}

func TestProtocolTerminalStepCannotExecuteAgain(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "settled"}[skipped], func(t *testing.T) {
			plan := NewPlan()
			job := &attemptJob{}
			Job(plan, "job", job)
			ops := sdkOperations{}
			prior := nodeproto.Request{Job: "job", Step: "source", Attempt: 1, Settled: []string{}}
			if skipped {
				prior.Step, prior.Settled = "result", []string{"source"}
			}
			result, err := ops.RunStep(t.Context(), plan, prior)
			if err != nil || result.Error != nil {
				t.Fatalf("first request: %+v, %v", result, err)
			}
			calls := job.calls
			_, err = ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: 1, Settled: []string{}})
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || job.calls != calls {
				t.Fatalf("terminal source executed again: calls=%d, want=%d, error=%v", job.calls, calls, err)
			}
			result, err = ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: 2, Settled: []string{}})
			if err != nil || result.Error != nil || job.calls != calls+1 {
				t.Fatalf("next attempt: %+v, calls=%d, error=%v", result, job.calls, err)
			}
		})
	}
}

func TestProtocolFailedBodyAlwaysSettlesBeforeReply(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "panic", true: "dry-run-refusal"}[dryRun], func(t *testing.T) {
			plan := NewPlan()
			calls := 0
			Job(plan, "job", func(context.Context) error {
				calls++
				panic("body failed")
			})
			ctx := context.WithValue(t.Context(), dryRunKey{}, dryRun)
			request := nodeproto.Request{Job: "job", Step: "run", Attempt: 1, Settled: []string{}}
			result, err := (sdkOperations{}).RunStep(ctx, plan, request)
			if err != nil || result.Outcome != "failed" || result.Error == nil {
				t.Fatalf("first body outcome: %+v, %v", result, err)
			}
			prior := calls
			_, err = (sdkOperations{}).RunStep(ctx, plan, request)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || calls != prior {
				t.Fatalf("terminal body was admitted again: calls=%d, want=%d, error=%v", calls, prior, err)
			}
		})
	}
}
