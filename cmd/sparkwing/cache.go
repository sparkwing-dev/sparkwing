package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/cachepressure"
)

var measureCachePressure = cachepressure.Measure
var pruneCachePressure = cachepressure.Prune

func runCache(args []string) error {
	if handleParentHelp(cmdCache, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCache, os.Stderr)
		return errors.New("cache: subcommand required (status|prune)")
	}
	switch args[0] {
	case "status":
		return runCacheStatus(args[1:])
	case "prune":
		return runCachePrune(args[1:])
	default:
		PrintHelp(cmdCache, os.Stderr)
		return errors.New("cache: unknown subcommand " + args[0])
	}
}

func runCacheStatus(args []string) error {
	fs := flag.NewFlagSet(cmdCacheStatus.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format (pretty|json)")
	if err := parseAndCheck(cmdCacheStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return writeCacheError(*output, fmt.Errorf("cache status: unexpected positional %q", fs.Arg(0)))
	}
	status, err := measureCachePressure(context.Background())
	if err != nil {
		return writeCacheError(*output, fmt.Errorf("cache status: %w", err))
	}
	return writeCacheOutput(*output, status, func() {
		fmt.Printf("managed: %s across %d entries (%s active across %d entries)\n",
			humanBytes(status.ObservedBytes), status.EntryCount,
			humanBytes(status.ActiveBytes), status.ActiveEntries)
		fmt.Printf("busy: %d entries\n", status.BusyEntries)
		fmt.Printf("legacy: %s across %d entries\n", humanBytes(status.LegacyBytes), status.LegacyEntries)
	})
}

func runCachePrune(args []string) error {
	fs := flag.NewFlagSet(cmdCachePrune.Path, flag.ContinueOnError)
	goalBytes := fs.Int64("goal-bytes", 0, "minimum bytes to reclaim")
	maxEntries := fs.Int("max-entries", 0, "maximum entries to examine")
	output := fs.StringP("output", "o", "pretty", "output format (pretty|json)")
	if err := parseAndCheck(cmdCachePrune, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return writeCacheError(*output, fmt.Errorf("cache prune: unexpected positional %q", fs.Arg(0)))
	}
	if *goalBytes <= 0 {
		return writeCacheError(*output, errors.New("cache prune: --goal-bytes must be greater than zero"))
	}
	if *maxEntries <= 0 {
		return writeCacheError(*output, errors.New("cache prune: --max-entries must be greater than zero"))
	}
	result, err := pruneCachePressure(context.Background(), cachepressure.PruneOptions{
		ReclaimBytes: *goalBytes,
		MaxEntries:   *maxEntries,
	})
	if err != nil {
		return writeCacheError(*output, fmt.Errorf("cache prune: %w", err))
	}
	return writeCacheOutput(*output, result, func() {
		fmt.Printf("reclaimed: %s across %d entries\n", humanBytes(result.ReclaimedBytes), result.Reclaimed)
		fmt.Printf("examined: %d, active: %d, busy: %d\n", result.Examined, result.Active, result.Busy)
		fmt.Printf("goal satisfied: %t\n", result.GoalSatisfied)
	})
}

func writeCacheError(output string, err error) error {
	if output != "json" {
		return err
	}
	encoded := struct {
		Payload any `json:"payload"`
		Error   any `json:"error"`
	}{Error: struct {
		Message string `json:"message"`
	}{Message: err.Error()}}
	return errors.Join(err, json.NewEncoder(os.Stdout).Encode(encoded))
}

func writeCacheOutput(output string, payload any, pretty func()) error {
	switch output {
	case "json":
		encoded := struct {
			Payload any `json:"payload"`
			Error   any `json:"error"`
		}{Payload: payload}
		return json.NewEncoder(os.Stdout).Encode(encoded)
	case "pretty", "":
		pretty()
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json)", output)
	}
}
