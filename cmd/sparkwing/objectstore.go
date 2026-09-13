package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runClusterObjectStore(args []string) error {
	if handleParentHelp(cmdClusterObjectStore, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdClusterObjectStore, os.Stdout)
		return nil
	}
	switch args[0] {
	case "status":
		return runClusterObjectStoreStatus(args[1:])
	case "reset-breaker":
		return runClusterObjectStoreResetBreaker(args[1:])
	default:
		PrintHelp(cmdClusterObjectStore, os.Stderr)
		return fmt.Errorf("cluster object-store: unknown subcommand %q", args[0])
	}
}

func runClusterObjectStoreStatus(args []string) error {
	fs := flag.NewFlagSet(cmdClusterObjectStoreStatus.Path, flag.ContinueOnError)
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdClusterObjectStoreStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	c, err := objectStoreClient(*on, "cluster object-store status")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := c.ObjectStoreBreakerState(ctx)
	if err != nil {
		return fmt.Errorf("cluster object-store status: %w", err)
	}
	if *outputFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(state)
	}
	return renderObjectStoreBreaker(state)
}

func objectStoreClient(on, cmd string) (*client.Client, error) {
	if on == "" {
		return nil, fmt.Errorf("%s: --profile is required", cmd)
	}
	prof, err := resolveProfile(on)
	if err != nil {
		return nil, err
	}
	if err := requireController(prof, cmd); err != nil {
		return nil, err
	}
	return client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()), nil
}

func runClusterObjectStoreResetBreaker(args []string) error {
	fs := flag.NewFlagSet(cmdClusterObjectStoreResetBreaker.Path, flag.ContinueOnError)
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdClusterObjectStoreResetBreaker, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	c, err := objectStoreClient(*on, "cluster object-store reset-breaker")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := c.ResetObjectStoreBreaker(ctx)
	if err != nil {
		return fmt.Errorf("cluster object-store reset-breaker: %w", err)
	}

	if *outputFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(state)
	}
	return renderObjectStoreBreaker(state)
}

func renderObjectStoreBreaker(state *client.ObjectStoreBreaker) error {
	fmt.Printf("breaker: enabled=%t tripped=%t reset=%s\n", state.Enabled, state.Tripped, state.Reset)
	if len(state.Cleared) > 0 {
		fmt.Printf("cleared: %v\n", state.Cleared)
	}
	renderObjectStoreCeiling(state.Ceiling, state.Thawed)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tPER-MINUTE\tPER-DAY\tMINUTE USED\tDAY USED\tTRIPS\tTRIPPED")
	for _, c := range state.Classes {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%t\n",
			c.Class, c.PerMinute, c.PerDay, c.MinuteUsed, c.DayUsed, c.Trips, c.Tripped)
	}
	return tw.Flush()
}

func renderObjectStoreCeiling(c client.ObjectStoreCeiling, thawed bool) {
	if !c.Enforced {
		fmt.Println("ceiling: unlimited")
		return
	}
	fmt.Printf("ceiling: frozen=%t warning=%t bytes=%d/%d objects=%d/%d\n",
		c.Frozen, c.Warning, c.Bytes, c.MaxBytes, c.Objects, c.MaxObjects)
	if c.Frozen {
		fmt.Printf("ceiling frozen on %s since %s\n", c.FrozenReason, c.FrozenAt.Format(time.RFC3339))
	}
	if c.Thawed {
		fmt.Println("ceiling thawed: the freeze is held off until the next measurement")
	}
	if !c.ReconciledAt.IsZero() {
		fmt.Printf("ceiling measured at %s every %s\n", c.ReconciledAt.Format(time.RFC3339), c.Reconcile)
	}
	if thawed {
		fmt.Println("thawed: the bucket ceiling freeze was cleared")
	}
}
