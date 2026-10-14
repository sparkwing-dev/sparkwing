// Command retryfixture is a pipeline the retry tests run twice: a source
// node generates two checks, "bad" fails on its first run, and a summary
// waits for the checks. The mode file changes what the generator returns
// between the runs: "changed" alters the good check's spec, and "hang"
// blocks the generator.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	ModeFile string `flag:"mode-file" desc:"File naming the generator's mode"`
	TraceDir string `flag:"trace-dir" desc:"Directory each body appends a line to"`
}

type pipeline struct{ sparkwing.Base }

func trace(dir, node string) {
	f, err := os.OpenFile(filepath.Join(dir, node), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString("body\n")
		_ = f.Close()
	}
}

type source struct {
	sparkwing.Produces[[]string]
	dir string
}

func (s source) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "discover", func(context.Context) ([]string, error) {
		trace(s.dir, "targets")
		return []string{"good", "bad"}, nil
	}), nil
}

type check struct {
	sparkwing.Produces[string]
	mode, id, dir string
}

func (c check) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "check-"+c.mode, func(context.Context) (string, error) {
		_, prior := os.Stat(filepath.Join(c.dir, c.id))
		trace(c.dir, c.id)
		if c.id == "bad" && prior != nil {
			return "", errors.New("the first bad check fails")
		}
		if c.id == "good" && c.mode == "changed" {
			return "", errors.New("the changed good check fails")
		}
		return c.mode, nil
	}), nil
}

func (pipeline) Plan(_ context.Context, p *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	targets := sparkwing.Job(p, "targets", source{dir: in.TraceDir}).Timeout(400 * time.Millisecond)
	checks := sparkwing.JobFanOutDynamic(p, "tests", targets, func(id string) (string, any) {
		mode, err := os.ReadFile(in.ModeFile)
		if err != nil {
			panic(err)
		}
		if strings.TrimSpace(string(mode)) == "hang" {
			select {}
		}
		return "test-" + id, check{mode: strings.TrimSpace(string(mode)), id: id, dir: in.TraceDir}
	})
	sparkwing.Job(p, "summary", func(context.Context) error {
		trace(in.TraceDir, "summary")
		return nil
	}).Needs(checks)
	return nil
}

func main() {
	sparkwing.Register[inputs]("probe", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	runner.Main()
}
