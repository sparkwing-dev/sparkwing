// Command podnodefixture is the pipeline the pod-side node host's tests run:
// one node that writes its environment to a file and prints a file's
// content; when asked, a plan that drifts between processes, a failing node
// with retries whose before_run and after_run calls are traced, and a node in a
// concurrency group.
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
}

type pipeline struct{ sparkwing.Base }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
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
			return errors.Join(err, f.Close())
		}).Memoize(func(context.Context) (sparkwing.CacheKey, error) { return "pod-memo-v1", nil })
		if in.Publish {
			memoized.Outputs("dist/*")
		}
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

func main() {
	sparkwing.Register[inputs]("podnode", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	runner.Main()
}
