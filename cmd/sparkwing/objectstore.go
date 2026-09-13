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
	case "reset-breaker":
		return runClusterObjectStoreResetBreaker(args[1:])
	default:
		PrintHelp(cmdClusterObjectStore, os.Stderr)
		return fmt.Errorf("cluster object-store: unknown subcommand %q", args[0])
	}
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
	if *on == "" {
		return errors.New("cluster object-store reset-breaker: --profile is required")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "cluster object-store reset-breaker"); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	state, err := c.ResetObjectStoreBreaker(ctx)
	if err != nil {
		return fmt.Errorf("cluster object-store reset-breaker: %w", err)
	}

	if *outputFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(state)
	}
	renderObjectStoreBreaker(state)
	return nil
}

func renderObjectStoreBreaker(state *client.ObjectStoreBreaker) {
	if len(state.Cleared) == 0 {
		fmt.Println("no object-store class was tripped; nothing to clear")
	} else {
		fmt.Printf("cleared: %v\n", state.Cleared)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tPER-MINUTE\tPER-DAY\tMINUTE USED\tDAY USED\tTRIPS\tTRIPPED")
	for _, c := range state.Classes {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%t\n",
			c.Class, c.PerMinute, c.PerDay, c.MinuteUsed, c.DayUsed, c.Trips, c.Tripped)
	}
	_ = tw.Flush()
}
