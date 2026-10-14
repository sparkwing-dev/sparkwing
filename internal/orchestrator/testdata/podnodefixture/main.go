// Command podnodefixture is the pipeline the pod-side node host's tests run:
// one node that writes its environment to a file and prints a file's
// content; when asked, a plan that drifts between processes, a failing node
// with retries whose before_run and after_run calls are traced, and a node in a
// concurrency group; when asked, a node that waits until it is stopped, and a
// deploy node whose skip_if reads a secret, and a failing node whose recovery
// records the failure it was handed; when asked, a source node whose output
// generates one node per target and a summary that needs them all; when asked,
// a ship node of three steps behind a prep node, beside a lint node. The
// trigger's actor and pull request, when set, reach the build node's
// environment as ACTOR.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	EnvOut  string `flag:"envout" desc:"File the node writes its environment to"`
	Echo    string `flag:"echo" desc:"File whose content the node prints"`
	Drift   string `flag:"drift" desc:"File counting plans; set, each plan differs"`
	Trace   string `flag:"trace" desc:"File the node's before_run and after_run calls append to; set, the node fails and retries twice"`
	Auto    bool   `flag:"auto" desc:"Retry the traced node with retry_auto"`
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
	Ranged  string `flag:"ranged" desc:"File a prep node, a lint node and a ship node of three steps append their names to"`
}

// shipJob's steps run compile, package, upload in order, each appending its
// name to the ranged file.
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
		if in.Auto {
			build.Retry(2, sparkwing.RetryAuto())
		} else {
			build.Retry(2)
		}
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
		}).Memoize(func(context.Context) (sparkwing.CacheKey, error) { return "pod-memo-v1", nil })
		if in.Publish {
			memoized.Outputs("dist/*")
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
		source.Memoize(func(context.Context) (sparkwing.CacheKey, error) { return "pod-fanout-v1", nil })
	}
	tests := sparkwing.JobFanOutDynamic(plan, "tests", source, func(target string) (string, any) {
		id := "test-" + target
		return id, func(context.Context) error {
			logBody(id)
			if _, err := os.Stat(in.FanFail); target == "fail" && err == nil {
				return errors.New("target fail is broken")
			}
			return nil
		}
	})
	return sparkwing.Job(plan, "summary", func(context.Context) error { logBody("summary"); return nil }).Needs(tests)
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
	runner.Main()
}
