package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	EnvOut  string `flag:"envout" desc:"File the node writes its environment to"`
	Echo    string `flag:"echo" desc:"File whose content the node prints"`
	Drift   string `flag:"drift" desc:"File counting plans; set, each plan differs"`
	Trace   string `flag:"trace" desc:"File the node's before_run and after_run calls append to; set, the node fails and retries twice"`
	Group   bool   `flag:"group" desc:"Put the node in a concurrency group"`
	Refuse  bool   `flag:"refuse" desc:"The traced node's before_run refuses"`
	Pass    string `flag:"pass" secret:"true" desc:"A secret the node prints"`
	Memo    string `flag:"memo" desc:"File a memoized node appends to each time its body runs"`
	Publish bool   `flag:"publish" desc:"The memoized node publishes an artifact"`
	Hold    string `flag:"hold" desc:"File a held node writes its pid to before it waits to be stopped"`
	Guard   string `flag:"guard" desc:"File a deploy node writes unless its skip_if, which reads DEPLOY_GUARD, says skip"`
	Recover string `flag:"recover" desc:"File a failing node's recovery writes the parent's error to"`
	FanOut  string `flag:"fanout" desc:"Comma-separated targets a source node lists, each generating a node a summary needs"`
	FanLog  string `flag:"fanlog" desc:"File each fan-out node's body appends its id to"`
	FanFail string `flag:"fanfail" desc:"File whose presence fails the node generated for target fail"`
	FanMemo bool   `flag:"fanmemo" desc:"Memoize the fan-out source"`
	Region  bool   `flag:"region" desc:"The build node's env carries the AWS_REGION plan code reads"`
	Scale   string `flag:"scaleout" desc:"File a scale node writes its job args and the zone input to"`
	Zone    string `flag:"zone" desc:"An input the scale node records"`
	Shards  string `flag:"shards" desc:"File an aggregator writes the artifacts of two memoized producers to"`
	Typed   bool   `flag:"typed" desc:"Add a node that returns a typed output"`
	Ranged  string `flag:"ranged" desc:"File a prep node, a lint node and a ship node of three steps append their names to"`
	Stage   string `flag:"stage" default:"prod" desc:"A value only guards read"`
}

type shipJob struct {
	sparkwing.Base
	out string
}

func (j *shipJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	var prev *sparkwing.WorkStep
	for _, id := range []string{"compile", "package", "upload"} {
		st := sparkwing.Step(w, id, func(context.Context) error { return appendLine(j.out, id) })
		if prev != nil {
			st.Needs(prev)
		}
		prev = st
	}
	return nil, nil
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

type typedJob struct {
	sparkwing.Base
	sparkwing.Produces[string]
}

func (typedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "emit", func(context.Context) (string, error) { return "typed output", nil }), nil
}

type scaleArgs struct {
	Replicas int `flag:"replicas" default:"1" desc:"Replica count the scale node records"`
}

type scaleJob struct {
	sparkwing.Base
	sparkwing.WithArgs[scaleArgs]
	out, zone string
}

func (j *scaleJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "apply", func(ctx context.Context) error {
		return os.WriteFile(j.out, fmt.Appendf(nil, "replicas=%d zone=%s", j.Args(ctx).Replicas, j.zone), 0o600)
	}), nil
}

func addShards(plan *sparkwing.Plan, out string) {
	producer := func(id, rel, content string) *sparkwing.JobNode {
		return sparkwing.Job(plan, id, func(context.Context) error {
			if err := os.MkdirAll("shards", 0o700); err != nil {
				return err
			}
			return os.WriteFile(rel, []byte(content), 0o600)
		}).Artifacts(rel).Memo(func(context.Context) (sparkwing.MemoValue, error) { return sparkwing.MemoKey(id, "v1"), nil })
	}
	one, two := producer("shard-1", "shards/1.txt", "one"), producer("shard-2", "shards/2.txt", "two")
	sparkwing.Job(plan, "aggregate", func(context.Context) error {
		a, errA := os.ReadFile("shards/1.txt")
		b, errB := os.ReadFile("shards/2.txt")
		if err := errors.Join(errA, errB); err != nil {
			return err
		}
		return os.WriteFile(out, []byte(string(a)+"+"+string(b)), 0o600)
	}).Consumes(one).Consumes(two)
}

type fixtureSecrets struct {
	Guard string `sw:"DEPLOY_GUARD,optional"`
}

type pipeline struct{ sparkwing.Base }

func (pipeline) Secrets() any { return &fixtureSecrets{} }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", func(ctx context.Context) error {
		echo, _ := os.ReadFile(in.Echo)
		fmt.Println("echo " + string(echo))
		fmt.Println("pass " + in.Pass)
		sparkwing.Info(ctx, "record %s", echo)
		if err := os.WriteFile(in.EnvOut, []byte(strings.Join(os.Environ(), "\n")), 0o600); err != nil {
			return err
		}
		if in.Trace != "" {
			return errors.New("still broken")
		}
		return nil
	})
	if in.Trace != "" {
		trace := func(line string) {
			f, err := os.OpenFile(in.Trace, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				_, _ = fmt.Fprintln(f, line)
				_ = f.Close()
			}
		}
		build.BeforeRun(func(context.Context) error {
			trace("before")
			if in.Refuse {
				return errors.New("not authorized")
			}
			return nil
		}).
			AfterRun(func(_ context.Context, err error) { trace(fmt.Sprintf("after:%v", err)) })
		build.Retry(2)
	}
	if in.Memo != "" {
		memoized := sparkwing.Job(plan, "memoized", func(context.Context) error {
			f, err := os.OpenFile(in.Memo, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(f, "body")
			if err = errors.Join(err, f.Close()); err != nil || !in.Publish {
				return err
			}
			if err := os.MkdirAll("dist", 0o700); err != nil {
				return err
			}
			return os.WriteFile("dist/out.txt", []byte("artifact\n"), 0o600)
		}).Memo(func(context.Context) (sparkwing.MemoValue, error) { return "pod-memo-v1", nil })
		if in.Publish {
			memoized.Artifacts("dist/*")
		}
	}
	var summary *sparkwing.JobNode
	if in.FanOut != "" {
		summary = fanOut(plan, in)
	}
	if in.Hold != "" {
		held := sparkwing.Job(plan, "held", func(ctx context.Context) error {
			if err := os.WriteFile(in.Hold, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}).Needs(build)
		if summary != nil {
			held.Needs(summary)
		}
	}
	if in.Guard != "" {
		sparkwing.Job(plan, "deploy", func(context.Context) error {
			return os.WriteFile(in.Guard, []byte("deployed"), 0o600)
		}).SkipIf(func(ctx context.Context) bool {
			guard, err := sparkwing.Secret(ctx, "DEPLOY_GUARD")
			if err != nil {
				panic(err)
			}
			return guard == "skip"
		})
	}
	if pr := rc.Trigger.PullRequest; rc.Trigger.User != "" || pr != nil {
		actor := rc.Trigger.User
		if pr != nil {
			actor += "#" + strconv.Itoa(pr.Number)
		}
		build.Env("ACTOR", actor)
	}
	if in.Recover != "" {
		sparkwing.Job(plan, "broken", func(context.Context) error { return errors.New("broken on purpose") }).
			OnFailure("recover", func(_ context.Context, f sparkwing.Failure) error {
				if f.Err == nil {
					return errors.New("the parent's failure was dropped")
				}
				return os.WriteFile(in.Recover, []byte(f.Err.Error()), 0o600)
			})
	}
	if in.Ranged != "" {
		prep := sparkwing.Job(plan, "prep", func(context.Context) error { return appendLine(in.Ranged, "prep") })
		sparkwing.Job(plan, "lint", func(context.Context) error { return appendLine(in.Ranged, "lint") })
		sparkwing.Job(plan, "ship", &shipJob{out: in.Ranged}).Needs(prep)
	}
	if in.Region {
		build.Env("PLAN_REGION", os.Getenv("AWS_REGION"))
	}
	if in.Scale != "" {
		sparkwing.Job(plan, "scale", &scaleJob{out: in.Scale, zone: in.Zone})
	}
	if in.Typed {
		sparkwing.Job(plan, "typed", typedJob{})
	}
	if in.Shards != "" {
		addShards(plan, in.Shards)
	}
	if in.Group {
		build.Concurrency(sparkwing.NewConcurrencyGroup("pod-group", sparkwing.ConcurrencyLimit{Capacity: 1}))
	}
	if in.Drift != "" {
		data, _ := os.ReadFile(in.Drift)
		n, _ := strconv.Atoi(string(data))
		n++
		if err := os.WriteFile(in.Drift, []byte(strconv.Itoa(n)), 0o600); err != nil {
			return err
		}
		build.Env("PLAN_SEQ", strconv.Itoa(n))
	}
	return nil
}

func fanOut(plan *sparkwing.Plan, in inputs) *sparkwing.JobNode {
	logBody := func(id string) {
		f, err := os.OpenFile(in.FanLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, id)
			_ = f.Close()
		}
	}
	source := sparkwing.Job(plan, "targets", &targetsJob{list: in.FanOut, log: logBody})
	if in.FanMemo {
		source.Memo(func(context.Context) (sparkwing.MemoValue, error) { return "pod-fanout-v1", nil })
	}
	targets := sparkwing.RefTo[[]string](source)
	tests := source.Generate(func(ctx context.Context, expansion *sparkwing.Plan) error {
		for _, target := range targets.Get(ctx) {
			id := "test-" + target
			sparkwing.Job(expansion, id, func(context.Context) error {
				logBody(id)
				if _, err := os.Stat(in.FanFail); target == "fail" && err == nil {
					return errors.New("target fail is broken")
				}
				return nil
			})
		}
		return nil
	})
	return sparkwing.Job(plan, "summary", func(context.Context) error { logBody("summary"); return nil }).NeedsGenerated(tests)
}

type targetsJob struct {
	sparkwing.Produces[[]string]
	list string
	log  func(string)
}

func (j *targetsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "list", func(context.Context) ([]string, error) {
		j.log("targets")
		return strings.Split(j.list, ","), nil
	}), nil
}

func main() {
	sparkwing.Register[inputs]("podnode", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	sparkwing.Main()
}
