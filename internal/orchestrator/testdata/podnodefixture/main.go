// Command podnodefixture is the pipeline the pod-side node host's tests run:
// one node that writes its environment to a file and prints a file's
// content, and a plan that drifts between processes when asked to.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	EnvOut string `flag:"envout" desc:"File the node writes its environment to"`
	Echo   string `flag:"echo" desc:"File whose content the node prints"`
	Drift  string `flag:"drift" desc:"File counting plans; set, each plan differs"`
}

type pipeline struct{ sparkwing.Base }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", func(ctx context.Context) error {
		echo, _ := os.ReadFile(in.Echo)
		fmt.Println("echo " + string(echo))
		sparkwing.Info(ctx, "record %s", echo)
		return os.WriteFile(in.EnvOut, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	})
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
