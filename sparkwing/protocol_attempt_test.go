package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type attemptJob struct {
	Base
	Produces[int]
	calls     int
	failFirst bool
}

func (j *attemptJob) Work(w *Work) (*WorkStep, error) {
	source := Step(w, "source", func(context.Context) (int, error) {
		j.calls++
		if j.failFirst && j.calls == 1 {
			return 0, errors.New("try again")
		}
		return j.calls, nil
	})
	return Step(w, "result", func(ctx context.Context) (int, error) {
		return StepGet[int](ctx, source), nil
	}).Needs(source), nil
}

func TestProtocolAttemptReplacesStepOutputs(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failFirst], func(t *testing.T) {
			plan := NewPlan()
			Job(plan, "job", &attemptJob{failFirst: failFirst})
			for attempt := 1; attempt <= 2; attempt++ {
				for _, step := range []string{"source", "result"} {
					settled := []string{}
					if step == "result" {
						settled = append(settled, "source")
					}
					got, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: step, Attempt: attempt, Settled: settled})
					if err != nil {
						t.Fatal(err)
					}
					if failFirst && attempt == 1 && step == "source" {
						if got.Error == nil {
							t.Fatal("first source attempt succeeded")
						}
						continue
					}
					want := attempt
					if failFirst && attempt == 1 {
						want = 0
					}
					var value int
					if json.Unmarshal(got.Output, &value) != nil || got.Error != nil || value != want {
						t.Fatalf("attempt %d %s = %+v, want %d", attempt, step, got, want)
					}
				}
			}
		})
	}
}

func TestProtocolAttemptForgetsSkippedDependencyOutput(t *testing.T) {
	plan := NewPlan()
	Job(plan, "job", &attemptJob{})
	if _, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: 1, Settled: []string{}}); err != nil {
		t.Fatal(err)
	}
	got, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "result", Attempt: 2, Settled: []string{"source"}})
	if err != nil || got.Error != nil || string(got.Output) != "0" {
		t.Fatalf("skipped source reused its previous output: %+v, %v", got, err)
	}
}

func TestProtocolAttemptCannotResetActiveWork(t *testing.T) {
	plan := NewPlan()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	Job(plan, "job", &parallelAttemptJob{started: started, release: release})
	finished := make(chan error, 1)
	go func() {
		_, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "wait", Attempt: 4, Settled: []string{}})
		finished <- err
	}()
	<-started
	got, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "run", Attempt: 4, Settled: []string{}})
	if err != nil || got.Error != nil || got.Outcome != "success" {
		t.Fatalf("parallel step within one attempt = %+v, %v", got, err)
	}
	_, err = (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "run", Attempt: 5, Settled: []string{}})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
		t.Fatalf("overlapping attempt = %v", err)
	}
	// safety: cleanup joins the body before the test's context is released.
	t.Cleanup(func() {
		if err := <-finished; err != nil {
			t.Error(err)
		}
	})
}

type parallelAttemptJob struct {
	Base
	started chan struct{}
	release chan struct{}
}

func (j *parallelAttemptJob) Work(w *Work) (*WorkStep, error) {
	Step(w, "wait", func(context.Context) error {
		close(j.started)
		<-j.release
		return nil
	})
	Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

func TestProtocolAttemptRejectsStaleOrdinal(t *testing.T) {
	plan := NewPlan()
	job := &attemptJob{}
	Job(plan, "job", job)
	if _, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: 4, Settled: []string{}}); err != nil {
		t.Fatal(err)
	}
	_, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "job", Step: "source", Attempt: 3, Settled: []string{}})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || job.calls != 1 {
		t.Fatalf("stale attempt = %v, body calls = %d", err, job.calls)
	}
}
