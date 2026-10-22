package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runOperatorObjectStore(inv clisurface.Invocation) error {
	return runOperatorObjectStoreCommand(context.Background(), inv, os.Stdout, resolveOperatorManager)
}

func runOperatorObjectStoreCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect operatorConnector) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing operator object-store" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	if err := ctx.Err(); err != nil {
		return err
	}
	var state controllerwire.ObjectStoreBreaker
	if inv.Command.Path == "sparkwing operator object-store reset-breaker" {
		state, err = manager.ResetObjectStoreBreaker(ctx, rand.Text())
	} else {
		state, err = manager.ReadObjectStoreBreaker(ctx)
	}
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(state)
	}
	return renderObjectStoreBreaker(out, state, format)
}

func renderObjectStoreBreaker(out io.Writer, state controllerwire.ObjectStoreBreaker, format string) error {
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "breaker: enabled=%t tripped=%t reset=%s\n", state.Enabled, state.Tripped, state.Reset)
	if len(state.Cleared) > 0 {
		fmt.Fprintf(&buffer, "cleared: %v\n", state.Cleared)
	}
	renderObjectStoreCeiling(&buffer, state.Ceiling)
	var rows io.Writer = &buffer
	var tw *tabwriter.Writer
	if format == "pretty" {
		tw = tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)
		rows = tw
		fmt.Fprintln(rows, "CLASS\tPER-MINUTE\tPER-DAY\tMINUTE USED\tDAY USED\tALLOWED\tREFUSED\tTRIPS\tTRIPPED")
	}
	for _, c := range state.Classes {
		fmt.Fprintf(rows, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%t\n", c.Class, c.PerMinute, c.PerDay, c.MinuteUsed, c.DayUsed, c.Allowed, c.Refused, c.Trips, c.Tripped)
	}
	if tw != nil {
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	_, err := out.Write(buffer.Bytes())
	return err
}

func renderObjectStoreCeiling(out io.Writer, c controllerwire.ObjectStoreCeiling) {
	if c.Enforced {
		fmt.Fprintf(out, "ceiling: frozen=%t warning=%t bytes=%d/%d objects=%d/%d\n", c.Frozen, c.Warning, c.Bytes, c.MaxBytes, c.Objects, c.MaxObjects)
	} else {
		fmt.Fprintf(out, "ceiling: unlimited bytes=%d objects=%d\n", c.Bytes, c.Objects)
	}
	if c.Incomplete {
		fmt.Fprintf(out, "ceiling measurement incomplete: %s\n", c.MeasureError)
	}
	if c.Frozen {
		fmt.Fprintf(out, "ceiling frozen on %s since %s\n", c.FrozenReason, c.FrozenAt.Format(time.RFC3339))
	}
	if !c.ReconciledAt.IsZero() {
		fmt.Fprintf(out, "ceiling measured at %s every %s\n", c.ReconciledAt.Format(time.RFC3339), c.Reconcile)
	}
}
