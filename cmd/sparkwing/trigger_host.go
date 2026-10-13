package main

import (
	"context"
	"errors"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// safety: the run's own host owns interrupts, so a cancel reaches its nodes and is recorded as one.
func runTriggerHost(args []string) error {
	fs := flag.NewFlagSet(orchestrator.TriggerHostVerb, flag.ContinueOnError)
	program := fs.String("program", "", "the compiled pipeline each node runs")
	sessions := fs.String("sessions", "", "the file each node process session is recorded in, for the claiming process to reap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *program == "" || fs.NArg() != 1 {
		return errors.New("usage: sparkwing " + orchestrator.TriggerHostVerb + " --program <pipeline> <trigger-id>")
	}
	return orchestrator.HostClaimedTrigger(context.Background(), fs.Arg(0), *program, *sessions)
}
