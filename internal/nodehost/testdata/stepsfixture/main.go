// Command stepsfixture is a pipeline whose nodes exercise each step
// semantic the host drives: needs and groups, parallel steps, fail-fast with
// a finally step, collect-all, continue-on-error, optional, skip_if, a step
// window and dry-run. Each step prints what it did, so a test reads the
// node log to see which bodies ran.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	Set string `flag:"set" desc:"main or dry"`
}

type pipeline struct{ sparkwing.Base }

type work func(w *sparkwing.Work) *sparkwing.WorkStep

func (f work) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) { return f(w), nil }

func say(format string, args ...any) func(context.Context) error {
	return func(context.Context) error {
		fmt.Printf(format+"\n", args...)
		return nil
	}
}

func fail(msg string) func(context.Context) error {
	return func(context.Context) error { return errors.New(msg) }
}

// safety: a bound on every wait, so a host that serializes steps fails the
// test rather than hanging it.
func wait(ctx context.Context, ch <-chan struct{}, what string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("never saw %s", what)
		}
		return ctx.Err()
	}
}

type orderJob struct{ sparkwing.Produces[string] }

func (orderJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	a := sparkwing.Step(w, "a", func(context.Context) (string, error) { return "from-a", nil })
	var checked atomic.Int32
	check := func(ctx context.Context) error { checked.Add(1); return nil }
	group := sparkwing.GroupSteps(w, "checks",
		sparkwing.Step(w, "c1", check), sparkwing.Step(w, "c2", check)).Needs(a)
	return sparkwing.Step(w, "b", func(ctx context.Context) (string, error) {
		got := sparkwing.StepGet[string](ctx, a)
		fmt.Printf("b saw %s after %d checks\n", got, checked.Load())
		return "result-" + got, nil
	}).Needs(group), nil
}

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	if in.Set == "dry" {
		sparkwing.Job(plan, "dry", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
			sparkwing.Step(w, "with-dry", say("real body")).DryRun(func(context.Context) error {
				fmt.Println("dry body")
				return nil
			})
			sparkwing.Step(w, "safe", say("safe ran")).SafeWithoutDryRun()
			return sparkwing.Step(w, "plain", say("plain ran"))
		}))
		return nil
	}

	sparkwing.Job(plan, "order", orderJob{})

	sparkwing.Job(plan, "parallel", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		var entered atomic.Int32
		both := make(chan struct{})
		var once sync.Once
		enter := func(ctx context.Context) error {
			if entered.Add(1) >= 2 {
				once.Do(func() { close(both) })
			}
			return wait(ctx, both, "a second step running at once")
		}
		sparkwing.Step(w, "p1", enter)
		sparkwing.Step(w, "p2", enter)
		return sparkwing.Step(w, "p3", enter)
	}))

	sparkwing.Job(plan, "failfast", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		setup := sparkwing.Step(w, "setup", func(context.Context) (string, error) { return "env-7", nil })
		started := make(chan struct{})
		slow := sparkwing.Step(w, "slow", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			fmt.Println("slow saw its cancel")
			return ctx.Err()
		}).Needs(setup)
		reject := sparkwing.Step(w, "reject", func(ctx context.Context) error {
			if err := wait(ctx, started, "slow start"); err != nil {
				return err
			}
			return errors.New("rejected")
		}).Needs(setup)
		pending := sparkwing.Step(w, "pending", say("pending ran")).Needs(slow)
		return sparkwing.Step(w, "cleanup", func(ctx context.Context) error {
			fmt.Println("cleanup saw " + sparkwing.StepGet[string](ctx, setup))
			return nil
		}).Needs(slow, reject, pending).Finally()
	}))

	sparkwing.Job(plan, "collect", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		w.FailurePolicy(sparkwing.CollectAll)
		independentStarted, prerequisiteFailed := make(chan struct{}), make(chan struct{})
		reject := sparkwing.Step(w, "reject", func(ctx context.Context) error {
			if err := wait(ctx, independentStarted, "independent start"); err != nil {
				return err
			}
			close(prerequisiteFailed)
			return errors.New("rejected")
		})
		sparkwing.Step(w, "independent", func(ctx context.Context) error {
			close(independentStarted)
			if err := wait(ctx, prerequisiteFailed, "reject fail"); err != nil {
				return err
			}
			fmt.Println("independent finished")
			return nil
		})
		return sparkwing.Step(w, "dependent", say("dependent ran")).Needs(reject)
	}))

	sparkwing.Job(plan, "continue", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		reject := sparkwing.Step(w, "reject", fail("rejected")).ContinueOnError()
		return sparkwing.Step(w, "dependent", say("dependent ran")).Needs(reject)
	}))

	sparkwing.Job(plan, "optional", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		sparkwing.Step(w, "best-effort", fail("nope")).Optional()
		return sparkwing.Step(w, "real-work", say("real work ran"))
	}))

	sparkwing.Job(plan, "skips", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		a := sparkwing.Step(w, "a", say("ran a")).SkipIf(func(context.Context) bool { return true })
		return sparkwing.Step(w, "b", say("ran b")).Needs(a)
	}))

	sparkwing.Job(plan, "skippanic", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		return sparkwing.Step(w, "guarded", say("guarded ran")).SkipIf(func(context.Context) bool { panic("guard blew up") })
	}))

	sparkwing.Job(plan, "panics", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		return sparkwing.Step(w, "oops", func(context.Context) error { panic("dropped a wrench") })
	}))

	sparkwing.Job(plan, "range", work(func(w *sparkwing.Work) *sparkwing.WorkStep {
		a := sparkwing.Step(w, "r-a", say("ran r-a"))
		b := sparkwing.Step(w, "r-b", say("ran r-b")).Needs(a)
		return sparkwing.Step(w, "r-c", say("ran r-c")).Needs(b)
	}))
	return nil
}

func main() {
	sparkwing.Register[inputs]("steps", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	sparkwing.Main()
}
