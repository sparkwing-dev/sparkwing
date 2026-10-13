// Command attemptfixture is a pipeline the attempt executor's tests run end
// to end: a node that reads a secret, prints it, registers a cleanup and
// returns an output; a node that exits mid-body; a plan that drifts
// between processes; nodes with every attempt closure; a dynamic fan-out
// with a node that waits for it; and memoized nodes.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"
)

type inputs struct {
	Marker  string `flag:"marker" desc:"Directory the cleanups touch files in"`
	Counter string `flag:"counter" desc:"File counting plans; set, each plan differs"`
	Crash   bool   `flag:"crash" desc:"Add a node that exits mid-body"`
	Leak    bool   `flag:"leak" desc:"Add a node whose error quotes the secret"`
	Hooks   bool   `flag:"hooks" desc:"Add nodes with skip_if, before_run, verify and after_run closures"`
	FanOut  string `flag:"fanout" desc:"Comma-separated targets to fan out over; boom fails the generator, hang blocks it"`
	Pass    string `flag:"pass" secret:"true" desc:"A secret input the build node prints"`
	Memo    bool   `flag:"memo" desc:"Add memoized nodes, one of them a fan-out source"`
}

type fixtureSecrets struct {
	Token string `sw:"DEPLOY_TOKEN,required"`
}

type pipeline struct{ sparkwing.Base }

func (pipeline) Secrets() any { return &fixtureSecrets{} }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &buildJob{in: in}).Env("FIXTURE_ENV", "from-plan")
	if in.Counter != "" {
		data, _ := os.ReadFile(in.Counter)
		n, _ := strconv.Atoi(string(data))
		n++
		if err := os.WriteFile(in.Counter, []byte(strconv.Itoa(n)), 0o600); err != nil {
			return err
		}
		build.Env("PLAN_SEQ", strconv.Itoa(n))
	}
	if in.Crash {
		sparkwing.Job(plan, "crash", func(ctx context.Context) error {
			if _, err := cleanup.Register(ctx, cleanup.Spec{Argv: []string{"touch", in.Marker + "/crash"}}); err != nil {
				return err
			}
			os.Exit(3)
			return nil
		})
	}
	if in.FanOut != "" {
		targets := sparkwing.Job(plan, "targets", &targetsJob{list: in.FanOut})
		tests := sparkwing.JobFanOutDynamic(plan, "tests", targets, func(target string) (string, any) {
			switch target {
			case "boom":
				panic("no target boom")
			case "hang":
				fmt.Println("generator hangs")
				select {}
			}
			return "test-" + target, func(context.Context) error { return nil }
		})
		sparkwing.Job(plan, "summary", func(context.Context) error { return nil }).Needs(tests)
	}
	if in.Leak {
		sparkwing.Job(plan, "leak", func(ctx context.Context) error {
			token, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
			if err != nil {
				return err
			}
			return fmt.Errorf("deploy refused token %s", token)
		})
	}
	if in.Hooks {
		addHooks(plan, in.Marker)
	}
	if in.Memo {
		addMemo(plan, in.Marker)
	}
	return nil
}

// addMemo adds memoized nodes whose bodies write "body" to <marker>/<node>.
func addMemo(plan *sparkwing.Plan, marker string) {
	key := func(k sparkwing.CacheKey, err error) sparkwing.CacheKeyFn {
		return func(context.Context) (sparkwing.CacheKey, error) { return k, err }
	}
	body := func(node string, fail bool) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			_, statErr := os.Stat(filepath.Join(marker, node))
			traceBody(marker, node)
			if fail && statErr != nil {
				return "", errors.New("first run fails")
			}
			return "from " + os.Getenv("SPARKWING_RUN_ID"), nil
		}
	}
	group := sparkwing.NewConcurrencyGroup("memo-group", sparkwing.ConcurrencyLimit{Capacity: 1, OnLimit: sparkwing.Queue})
	sparkwing.Job(plan, "memoized", &memoJob{body: body("memoized", false)}).Memoize(key("memo-v1", nil)).Concurrency(group)
	sparkwing.Job(plan, "memo-flaky", &memoJob{body: body("memo-flaky", true)}).Memoize(key("flaky-v1", nil))
	sparkwing.Job(plan, "nocache", &memoJob{body: body("nocache", false)}).Memoize(key(sparkwing.NoCache, nil))
	sparkwing.Job(plan, "emptykey", &memoJob{body: body("emptykey", false)}).Memoize(key("", nil))
	sparkwing.Job(plan, "keyfail", &memoJob{body: body("keyfail", false)}).Memoize(key("", errors.New("no lockfile")))
	source := sparkwing.Job(plan, "memo-targets", &targetsJob{list: "x,y", marker: marker}).Memoize(key("targets-v1", nil))
	tests := sparkwing.JobFanOutDynamic(plan, "memo-tests", source, func(target string) (string, any) {
		return "memo-test-" + target, func(context.Context) error { return nil }
	})
	sparkwing.Job(plan, "memo-summary", func(context.Context) error { return nil }).Needs(tests)
}

func traceBody(marker, node string) {
	f, err := os.OpenFile(filepath.Join(marker, node), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = fmt.Fprintln(f, "body")
		_ = f.Close()
	}
}

type memoJob struct {
	sparkwing.Produces[string]
	body func(context.Context) (string, error)
}

func (j *memoJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.body), nil
}

// addHooks adds nodes that write each closure's call to <marker>/<node>, one
// line per call.
func addHooks(plan *sparkwing.Plan, marker string) {
	trace := func(node, line string) {
		f, err := os.OpenFile(filepath.Join(marker, node), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, line)
			_ = f.Close()
		}
	}
	after := func(node string) sparkwing.AfterRunFn {
		return func(_ context.Context, err error) { trace(node, fmt.Sprintf("after:%v", err)) }
	}
	sparkwing.Job(plan, "skipped", func(context.Context) error { trace("skipped", "body"); return nil }).
		SkipIf(func(context.Context) bool { return false }).
		SkipIf(func(context.Context) bool { return true })
	sparkwing.Job(plan, "kept", func(context.Context) error { trace("kept", "body"); return nil }).
		SkipIf(func(context.Context) bool { panic("no answer") }).
		SkipIf(func(ctx context.Context) bool { <-ctx.Done(); return true }, sparkwing.SkipBudget(300*time.Millisecond))
	sparkwing.Job(plan, "hooked", func(context.Context) error {
		data, _ := os.ReadFile(filepath.Join(marker, "hooked"))
		trace("hooked", "body")
		if !strings.Contains(string(data), "body") {
			return errors.New("first try fails")
		}
		return nil
	}).
		BeforeRun(func(context.Context) error { trace("hooked", "before"); return nil }).
		Verify(func(context.Context) error { trace("hooked", "verify"); return nil }).
		AfterRun(after("hooked"))
	sparkwing.Job(plan, "unverified", func(context.Context) error { trace("unverified", "body"); return nil }).
		Verify(func(context.Context) error { return errors.New("health check: 503") }).
		AfterRun(after("unverified"))
	sparkwing.Job(plan, "unready", func(context.Context) error { trace("unready", "body"); return nil }).
		BeforeRun(func(context.Context) error { trace("unready", "before"); return errors.New("not ready") }).
		AfterRun(after("unready")).
		Retry(2)
	sparkwing.Job(plan, "flaky", func(context.Context) error { trace("flaky", "body"); return errors.New("still broken") }).
		BeforeRun(func(context.Context) error { trace("flaky", "before"); return nil }).
		AfterRun(after("flaky")).
		Retry(2)
	sparkwing.Job(plan, "slow", func(ctx context.Context) error { trace("slow", "body"); <-ctx.Done(); return ctx.Err() }).
		AfterRun(after("slow")).
		Timeout(500 * time.Millisecond)
	sparkwing.JobApproval(plan, "skipped-gate", sparkwing.ApprovalConfig{Message: "ship it?"}).
		SkipIf(func(context.Context) bool { return true })
	source := sparkwing.Job(plan, "unverified-source", &targetsJob{list: "a"}).
		Verify(func(context.Context) error { return errors.New("bad target list") })
	sparkwing.JobFanOutDynamic(plan, "unverified-fanout", source, func(target string) (string, any) {
		trace("unverified-source", "generated")
		return "unverified-" + target, func(context.Context) error { return nil }
	})
}

type buildOutput struct {
	Env  string `json:"env"`
	Run  string `json:"run"`
	Leak string `json:"leak"`
	Gate string `json:"gate"`
	PG   string `json:"pg"`
	AWS  string `json:"aws"`
}

type buildJob struct {
	sparkwing.Produces[buildOutput]
	in inputs
}

func (j *buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (buildOutput, error) {
		token, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
		if err != nil {
			return buildOutput{}, err
		}
		fmt.Println("stdout token=" + token)
		if j.in.Pass != "" {
			fmt.Println("stdout pass=" + j.in.Pass)
		}
		sparkwing.Info(ctx, "record token=%s", token)
		if j.in.Marker != "" {
			if _, err := cleanup.Register(ctx, cleanup.Spec{Argv: []string{"sh", "-c", "echo \"$AWS_SECRET_ACCESS_KEY\" > " + j.in.Marker + "/build; echo cleaned " + token}}); err != nil {
				return buildOutput{}, err
			}
		}
		return buildOutput{Env: os.Getenv("FIXTURE_ENV"), Run: os.Getenv("SPARKWING_RUN_ID"), Leak: os.Getenv("SPARKWING_AGENT_TOKEN"), Gate: os.Getenv("SPARKWING_GATE_INDEX"), PG: os.Getenv("SPARKWING_TEST_PG_URL"), AWS: os.Getenv("AWS_SECRET_ACCESS_KEY")}, nil
	}), nil
}

type targetsJob struct {
	sparkwing.Produces[[]string]
	list string
	// marker, when set, is where the body writes "body" to <marker>/<node>.
	marker string
}

func (j *targetsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "list", func(ctx context.Context) ([]string, error) {
		if j.marker != "" {
			traceBody(j.marker, sparkwing.NodeFromContext(ctx))
		}
		return strings.Split(j.list, ","), nil
	}), nil
}

func main() {
	sparkwing.Register[inputs]("attempts", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	runner.Main()
}
