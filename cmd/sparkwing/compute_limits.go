package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

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
		Runners            *int64           `json:"runners,omitempty"`
		ByPrincipal        map[string]int64 `json:"by_principal,omitempty"`
		AlarmReached       *bool            `json:"alarm_reached,omitempty"`
		DerivedRunnerCap   int64            `json:"derived_runner_cap,omitempty"`
		RecentPaidMicro    int64            `json:"recent_paid_micro"`
		ScaleWindowSeconds int64            `json:"scale_window_seconds,omitempty"`
	} `json:"usage"`
	Budgets struct {
		ClaimsPerRunnerMinute     int64 `json:"claims_per_runner_minute"`
		HeartbeatsPerRunnerMinute int64 `json:"heartbeats_per_runner_minute"`
		IdleClaimPollSeconds      int64 `json:"idle_claim_poll_seconds"`
		IdleClaimPollEnforced     bool  `json:"idle_claim_poll_enforced"`
		MaxLogStreamsPerPrincipal int64 `json:"max_log_streams_per_principal"`
		MaxDownloadsPerPrincipal  int64 `json:"max_downloads_per_principal"`
		RequestsPerTokenMinute    int64 `json:"requests_per_token_minute"`
		RequestsPerMinuteAlarm    int64 `json:"requests_per_minute_alarm"`
	} `json:"budgets"`
}

// safety: the budgets are process configuration the controller reports beside
// the stored guards, so they are named here in the order an operator sizes
// them rather than sorted with the guards `limits set` writes.
func budgetRows(view computeLimitsResp) [][2]string {
	idle := computeLimitLabel(view.Budgets.IdleClaimPollSeconds)
	if view.Budgets.IdleClaimPollSeconds > 0 {
		idle = strconv.FormatInt(view.Budgets.IdleClaimPollSeconds, 10) + "s"
		if view.Budgets.IdleClaimPollEnforced {
			idle += ", enforced"
		}
	}
	return [][2]string{
		{"claims_per_runner_minute", computeLimitLabel(view.Budgets.ClaimsPerRunnerMinute)},
		{"heartbeats_per_runner_minute", computeLimitLabel(view.Budgets.HeartbeatsPerRunnerMinute)},
		{"idle_claim_poll", idle},
		{"max_log_streams_per_principal", computeLimitLabel(view.Budgets.MaxLogStreamsPerPrincipal)},
		{"max_downloads_per_principal", computeLimitLabel(view.Budgets.MaxDownloadsPerPrincipal)},
		{"requests_per_token_minute", computeLimitLabel(view.Budgets.RequestsPerTokenMinute)},
		{"requests_per_minute_alarm", computeLimitLabel(view.Budgets.RequestsPerMinuteAlarm)},
	}
}

func runComputeLimitsShow(args []string) error {
	fs := flag.NewFlagSet(cmdLimitsShow.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	outputFormat := fs.StringP("output", "o", "",
		"output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	if err := parseAndCheck(cmdLimitsShow, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outputFormat, cmdLimitsShow.Path)
	if err != nil {
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
	if format == "json" {
		_, err := os.Stdout.Write(append(resp, '\n'))
		return err
	}
	var view computeLimitsResp
	if err := json.Unmarshal(resp, &view); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if format == "plain" {
		return writeComputeLimitsPlain(os.Stdout, view)
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
	if view.Usage.DerivedRunnerCap > 0 {
		fmt.Fprintf(tw, "DERIVED RUNNER CAP\t%s\n", derivedRunnerCapLabel(view))
	}
	if view.Usage.Runners != nil {
		fmt.Fprintf(tw, "CLOUD RUNNERS\t%d claimed now\n", *view.Usage.Runners)
	}
	for _, row := range budgetRows(view) {
		fmt.Fprintf(tw, "%s\t%s\n", strings.ToUpper(row[0]), row[1])
	}
	if view.Usage.AlarmReached != nil && *view.Usage.AlarmReached {
		fmt.Fprintf(tw, "ALARM\treached\n")
	}
	for _, principal := range sortedPrincipals(view.Usage.ByPrincipal) {
		fmt.Fprintf(tw, "  %s\t%d\n", principal, view.Usage.ByPrincipal[principal])
	}
	return tw.Flush()
}

func writeComputeLimitsPlain(w io.Writer, view computeLimitsResp) error {
	for _, name := range store.ComputeLimitNames() {
		if _, err := fmt.Fprintf(w, "%s\t%d\n", name, view.Limits[name]); err != nil {
			return err
		}
	}
	if view.Usage.DerivedRunnerCap > 0 {
		if _, err := fmt.Fprintf(w, "derived_runner_cap\t%d\n", view.Usage.DerivedRunnerCap); err != nil {
			return err
		}
	}
	for _, row := range budgetRows(view) {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	if view.Usage.Runners != nil {
		if _, err := fmt.Fprintf(w, "runners\t%d\n", *view.Usage.Runners); err != nil {
			return err
		}
	}
	for _, principal := range sortedPrincipals(view.Usage.ByPrincipal) {
		if _, err := fmt.Fprintf(w, "runners.%s\t%d\n", principal, view.Usage.ByPrincipal[principal]); err != nil {
			return err
		}
	}
	return nil
}

func derivedRunnerCapLabel(view computeLimitsResp) string {
	if view.Usage.ScaleWindowSeconds <= 0 {
		return strconv.FormatInt(view.Usage.DerivedRunnerCap, 10)
	}
	window := time.Duration(view.Usage.ScaleWindowSeconds) * time.Second
	return fmt.Sprintf("%d, from %s paid in the last %d days",
		view.Usage.DerivedRunnerCap, store.FormatCredits(view.Usage.RecentPaidMicro),
		int64(window/(24*time.Hour)))
}

func sortedPrincipals(held map[string]int64) []string {
	out := make([]string, 0, len(held))
	for principal := range held {
		out = append(out, principal)
	}
	sort.Strings(out)
	return out
}

func computeLimitLabel(value int64) string {
	if value <= 0 {
		return "unlimited"
	}
	return strconv.FormatInt(value, 10)
}
