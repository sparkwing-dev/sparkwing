package sparkwingtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type settledPredicateJob struct {
	sparkwing.Base
	entered chan struct{}
	seen    int
	fail    bool
}

func (j *settledPredicateJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	source := sparkwing.Step(w, "source", func(context.Context) (int, error) { return 42, nil }).SkipIf(func(context.Context) bool {
		if j.fail {
			panic(errors.New("predicate failed"))
		}
		return true
	})
	sparkwing.Step(w, "consumer", func(context.Context) error { return nil }).Needs(source).SkipIf(func(ctx context.Context) bool {
		close(j.entered)
		j.seen = sparkwing.StepGet[int](ctx, source)
		return j.seen == 0
	})
	return nil, nil
}

func TestFailedStepPredicateSettlesItsPredecessorState(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	plan := sparkwing.NewPlan()
	job := &settledPredicateJob{entered: make(chan struct{}), fail: true}
	sparkwing.Job(plan, "job", job)
	if _, err := host.Eval(t.Context(), plan, "job", "job/step/source/skip/0", nil); err == nil {
		t.Fatal("source predicate succeeded")
	}
	if result, err := host.Eval(t.Context(), plan, "job", "job/step/consumer/skip/0", nil); err != nil || string(result) != "true" || job.seen != 0 {
		t.Fatalf("failed predecessor in consumer predicate: %s, %v, observed=%d", result, err, job.seen)
	}
}

func TestStepPredicateObservesSkippedPredecessor(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	for _, skipped := range []bool{true, false} {
		t.Run(map[bool]string{true: "skipped", false: "unsettled"}[skipped], func(t *testing.T) {
			plan := sparkwing.NewPlan()
			job := &settledPredicateJob{entered: make(chan struct{})}
			sparkwing.Job(plan, "job", job)
			if skipped {
				if result, err := host.Eval(t.Context(), plan, "job", "job/step/source/skip/0", nil); err != nil || string(result) != "true" {
					t.Fatalf("source predicate: %s, %v", result, err)
				}
				result, err := host.Eval(t.Context(), plan, "job", "job/step/consumer/skip/0", nil)
				if err != nil || string(result) != "true" || job.seen != 0 {
					t.Fatalf("consumer predicate: %s, %v, observed=%d", result, err, job.seen)
				}
				return
			}
			// safety: entering the predicate cancels the evaluation, so one that wrongly
			// runs before its predecessor settles fails the test instead of hanging it.
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
			result, err := host.Eval(ctx, plan, "job", "job/step/consumer/skip/0", nil)
			cancel()
			<-joined
			if err == nil {
				t.Fatalf("predicate accepted an unsettled predecessor: %s", result)
			}
			select {
			case <-job.entered:
				t.Fatal("predicate ran before its predecessor settled")
			default:
			}
		})
	}
}
