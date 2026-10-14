package main

import (
	"context"
	"errors"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// safety: the run's own host owns interrupts, so a cancel reaches its nodes and is recorded as one.
func runTriggerHost(args []string) error {
	fs := flag.NewFlagSet(orchestrator.TriggerHostVerb, flag.ContinueOnError)
	program := fs.String("program", "", "the compiled pipeline each node runs")
	direct := fs.Bool("direct", false, "host the trigger in this process rather than under a shepherd that reaps what it leaves")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *program == "" || fs.NArg() != 1 {
		return errors.New("usage: sparkwing " + orchestrator.TriggerHostVerb + " --program <pipeline> <trigger-id>")
	}
	if !*direct {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		argv := []string{self, orchestrator.TriggerHostVerb, "--direct", "--program", *program, fs.Arg(0)}
		if shepherded, err := orchestrator.ShepherdTrigger(argv); shepherded {
			return err
		}
	}
	return orchestrator.HostClaimedTrigger(context.Background(), fs.Arg(0), *program)
}
