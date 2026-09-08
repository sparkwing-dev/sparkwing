package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const straySessionSweepBudget = 15 * time.Second

// sweepStraySessionsBeforeRun ends step sessions left by nodes that died
// without reaping them, before this run competes with them for the machine.
// It never blocks a run: a sweep that fails is reported and the run goes on.
func sweepStraySessionsBeforeRun() {
	p, err := paths.DefaultPaths()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), straySessionSweepBudget)
	defer cancel()
	outcomes, err := sessionledger.Open(p.SessionLedgerDir()).Sweep(ctx, sessionledger.SweepOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, color.Dim("==> stray step sessions: sweep failed: "+err.Error()))
	}
	for _, o := range outcomes {
		switch o.Verdict {
		case sessionledger.VerdictReaped:
			fmt.Fprintln(os.Stderr, color.Dim(fmt.Sprintf("==> ended a stray step session from %s/%s (node %d gone): %s",
				o.Record.Run, o.Record.Node, o.Record.OwnerPID, o.Record.Command)))
		case sessionledger.VerdictFailed:
			fmt.Fprintln(os.Stderr, color.Dim(fmt.Sprintf("==> could not end a stray step session from %s/%s: %v",
				o.Record.Run, o.Record.Node, o.Err)))
		}
	}
	if !sessionledger.Acted(outcomes) {
		return
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		return
	}
	defer func() { _ = st.Close() }()
	for _, err := range sessionledger.RecordOutcomes(ctx, st, "run-start", outcomes) {
		fmt.Fprintln(os.Stderr, color.Dim("==> stray step sessions: record event: "+err.Error()))
	}
}
