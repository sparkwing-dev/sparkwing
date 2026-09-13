package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runComputeLimits(args []string) error {
	if handleParentHelp(cmdLimits, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdLimits, os.Stderr)
		return errors.New("limits: subcommand required (show|set)")
	}
	switch args[0] {
	case "show":
		return runComputeLimitsShow(args[1:])
	case "set":
		return runComputeLimitsSet(args[1:])
	default:
		PrintHelp(cmdLimits, os.Stderr)
		return fmt.Errorf("limits: unknown subcommand %q", args[0])
	}
}

type computeLimitsResp struct {
	Limits map[string]int64 `json:"limits"`
	Usage  struct {
		Runners      int64            `json:"runners"`
		ByPrincipal  map[string]int64 `json:"by_principal,omitempty"`
		AlarmReached bool             `json:"alarm_reached"`
	} `json:"usage"`
}

func runComputeLimitsShow(args []string) error {
	fs := flag.NewFlagSet(cmdLimitsShow.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdLimitsShow, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "cluster limits show"); err != nil {
		return err
	}
	resp, err := tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/compute-limits")
	if err != nil {
		return err
	}
	if *outputFormat == "json" {
		_, err := os.Stdout.Write(append(resp, '\n'))
		return err
	}
	var view computeLimitsResp
	if err := json.Unmarshal(resp, &view); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return renderComputeLimits(os.Stdout, view)
}

func runComputeLimitsSet(args []string) error {
	fs := flag.NewFlagSet(cmdLimitsSet.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	name := fs.String("name", "", "guard name")
	value := fs.Int64("value", -1, "ceiling; 0 removes it")
	if err := parseAndCheck(cmdLimitsSet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if !store.ValidComputeLimit(*name) {
		return fmt.Errorf("limits set: --name must be one of %s",
			strings.Join(store.ComputeLimitNames(), ", "))
	}
	if *value < 0 {
		return errors.New("limits set: --value must be zero or a positive number")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "cluster limits set"); err != nil {
		return err
	}
	body := map[string]any{"limits": map[string]int64{*name: *value}}
	resp, err := tokensPut(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/compute-limits", body)
	if err != nil {
		return err
	}
	var view computeLimitsResp
	if err := json.Unmarshal(resp, &view); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	fmt.Printf("%s = %s\n", *name, computeLimitLabel(view.Limits[*name]))
	return nil
}

func renderComputeLimits(w io.Writer, view computeLimitsResp) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range store.ComputeLimitNames() {
		fmt.Fprintf(tw, "%s\t%s\n", strings.ToUpper(name), computeLimitLabel(view.Limits[name]))
	}
	fmt.Fprintf(tw, "CLOUD RUNNERS\t%d claimed now\n", view.Usage.Runners)
	if view.Usage.AlarmReached {
		fmt.Fprintf(tw, "ALARM\treached\n")
	}
	for principal, held := range view.Usage.ByPrincipal {
		fmt.Fprintf(tw, "  %s\t%d\n", principal, held)
	}
	return tw.Flush()
}

func computeLimitLabel(value int64) string {
	if value <= 0 {
		return "unlimited"
	}
	return strconv.FormatInt(value, 10)
}
