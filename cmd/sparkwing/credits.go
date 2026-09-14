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

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runCredits(args []string) error {
	if handleParentHelp(cmdCredits, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCredits, os.Stderr)
		return fmt.Errorf("credits: subcommand required (show|grant|history|settings)")
	}
	switch args[0] {
	case "show":
		return runCreditsShow(args[1:])
	case "grant":
		return runCreditsGrant(args[1:])
	case "history":
		return runCreditsHistory(args[1:])
	case "settings":
		return runCreditsSettings(args[1:])
	default:
		PrintHelp(cmdCredits, os.Stderr)
		return fmt.Errorf("credits: unknown subcommand %q", args[0])
	}
}

type creditStateResp struct {
	BalanceMicro       int64  `json:"balance_micro"`
	GrantedMicro       int64  `json:"granted_micro"`
	ChargedMicro       int64  `json:"charged_micro"`
	RateMicroPerSecond int64  `json:"rate_micro_per_second"`
	GraceSeconds       int64  `json:"grace_seconds"`
	MaxChargeSeconds   int64  `json:"max_charge_seconds"`
	BurnWindowSeconds  int64  `json:"burn_window_seconds"`
	BurnMicro          int64  `json:"burn_micro"`
	ExhaustedAt        *int64 `json:"exhausted_at,omitempty"`
	MicroPerCredit     int64  `json:"micro_per_credit"`
	CreditsPerDollar   int64  `json:"credits_per_dollar"`
}

func runCreditsShow(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsShow.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdCreditsShow, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "credits show"); err != nil {
		return err
	}
	resp, err := tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits")
	if err != nil {
		return err
	}
	var state creditStateResp
	if err := json.Unmarshal(resp, &state); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if *outputFormat == "json" {
		_, err := os.Stdout.Write(append(resp, '\n'))
		return err
	}
	return renderCreditState(os.Stdout, state)
}

func renderCreditState(w io.Writer, state creditStateResp) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "BALANCE\t%s credits\n", store.FormatCredits(state.BalanceMicro))
	fmt.Fprintf(tw, "GRANTED\t%s credits\n", store.FormatCredits(state.GrantedMicro))
	fmt.Fprintf(tw, "CHARGED\t%s credits\n", store.FormatCredits(state.ChargedMicro))
	fmt.Fprintf(tw, "RATE\t%s credits per cloud runner second\n",
		creditsPerUnit(state.RateMicroPerSecond, state.MicroPerCredit))
	fmt.Fprintf(tw, "BURN (%s)\t%s credits\n",
		burnWindowLabel(state.BurnWindowSeconds), store.FormatCredits(state.BurnMicro))
	fmt.Fprintf(tw, "GRACE\t%ds past a node's claim reservation\n", state.GraceSeconds)
	fmt.Fprintf(tw, "CHARGE CAP\t%ds billed by any one charge\n", state.MaxChargeSeconds)
	if state.ExhaustedAt != nil {
		fmt.Fprintf(tw, "EXHAUSTED\t%s\n",
			time.Unix(*state.ExhaustedAt, 0).UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return tw.Flush()
}

func burnWindowLabel(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	label := time.Duration(seconds * int64(time.Second)).String()
	label = strings.TrimSuffix(label, "0s")
	return strings.TrimSuffix(label, "0m")
}

// safety: micro-credits carry six places, so a sub-credit rate needs all six.
func creditsPerUnit(micro, perCredit int64) string {
	if perCredit <= 0 {
		return strconv.FormatInt(micro, 10)
	}
	whole := micro / perCredit
	frac := micro % perCredit
	if frac < 0 {
		frac = -frac
	}
	return fmt.Sprintf("%d.%06d", whole, frac)
}

type creditSettingsResp struct {
	RateMicroPerSecond int64 `json:"rate_micro_per_second"`
	GraceSeconds       int64 `json:"grace_seconds"`
	MaxChargeSeconds   int64 `json:"max_charge_seconds"`
	MicroPerCredit     int64 `json:"micro_per_credit"`
	CreditsPerDollar   int64 `json:"credits_per_dollar"`
}

func runCreditsSettings(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsSettings.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	rate := fs.Int64("rate-micro", 0, "micro-credits one cloud runner second costs")
	grace := fs.Int64("grace-seconds", 0, "seconds a running node survives an empty balance")
	maxCharge := fs.Int64("max-charge-seconds", 0, "the most seconds any one charge may bill")
	outputFormat := fs.StringP("output", "o", "",
		"output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	if err := parseAndCheck(cmdCreditsSettings, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outputFormat, cmdCreditsSettings.Path)
	if err != nil {
		return err
	}
	body := creditSettingsBody(fs, *rate, *grace, *maxCharge)
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "cluster credits settings"); err != nil {
		return err
	}
	resp, err := creditSettingsExchange(prof, body)
	if err != nil {
		return err
	}
	if format == "json" {
		_, err := os.Stdout.Write(append(resp, '\n'))
		return err
	}
	var view creditSettingsResp
	if err := json.Unmarshal(resp, &view); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if format == "plain" {
		return writeCreditSettingsPlain(os.Stdout, view)
	}
	return renderCreditSettings(os.Stdout, view)
}

// safety: an unset flag is left out of the body so the controller keeps that
// setting, which is what makes changing one value a one-flag call. The store
// owns what each value may be, so the CLI sends what it was given and reports
// the refusal rather than holding a second copy of the bounds.
func creditSettingsBody(fs *flag.FlagSet, rate, grace, maxCharge int64) map[string]any {
	body := map[string]any{}
	if fs.Changed("rate-micro") {
		body["rate_micro_per_second"] = rate
	}
	if fs.Changed("grace-seconds") {
		body["grace_seconds"] = grace
	}
	if fs.Changed("max-charge-seconds") {
		body["max_charge_seconds"] = maxCharge
	}
	return body
}

func creditSettingsExchange(prof *profile.Profile, body map[string]any) ([]byte, error) {
	if len(body) == 0 {
		return tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/settings")
	}
	return tokensPut(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/settings", body)
}

func renderCreditSettings(w io.Writer, view creditSettingsResp) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "RATE\t%s credits per cloud runner second (%d micro)\n",
		creditsPerUnit(view.RateMicroPerSecond, view.MicroPerCredit), view.RateMicroPerSecond)
	fmt.Fprintf(tw, "GRACE\t%ds past a node's claim reservation\n", view.GraceSeconds)
	fmt.Fprintf(tw, "CHARGE CAP\t%ds billed by any one charge\n", view.MaxChargeSeconds)
	return tw.Flush()
}

func writeCreditSettingsPlain(w io.Writer, view creditSettingsResp) error {
	_, err := fmt.Fprintf(w, "rate_micro_per_second\t%d\ngrace_seconds\t%d\nmax_charge_seconds\t%d\n",
		view.RateMicroPerSecond, view.GraceSeconds, view.MaxChargeSeconds)
	return err
}

type creditGrantResp struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AmountMicro int64  `json:"amount_micro"`
	Reference   string `json:"reference,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func runCreditsGrant(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsGrant.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	kind := fs.String("kind", "", "grant kind: free|paid")
	amount := fs.Int64("amount", 0, "credits to add (100 credits = one dollar)")
	reference := fs.String("reference", "", "payment id or operator note recorded with the grant")
	if err := parseAndCheck(cmdCreditsGrant, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if !store.ValidCreditGrantKind(*kind) {
		return fmt.Errorf("credits grant: --kind must be free or paid")
	}
	if *amount <= 0 {
		return fmt.Errorf("credits grant: --amount must be a positive number of credits")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "credits grant"); err != nil {
		return err
	}
	body := map[string]any{
		"kind":         *kind,
		"amount_micro": *amount * store.MicroCreditsPerCredit,
	}
	if *reference != "" {
		body["reference"] = *reference
	}
	resp, err := tokensPost(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/grants", body)
	if err != nil {
		return err
	}
	var grant creditGrantResp
	if err := json.Unmarshal(resp, &grant); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	fmt.Printf("granted %s credits (%s) as %s\n",
		store.FormatCredits(grant.AmountMicro), grant.Kind, grant.ID)
	return nil
}

type creditChargeResp struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	NodeID      string `json:"node_id"`
	TokenPrefix string `json:"token_prefix"`
	Kind        string `json:"kind"`
	Seconds     int64  `json:"seconds"`
	AmountMicro int64  `json:"amount_micro"`
	ChargedAt   int64  `json:"charged_at"`
}

type creditHistoryResp struct {
	Grants  []creditGrantResp  `json:"grants"`
	Charges []creditChargeResp `json:"charges"`
}

const creditGrantRowType = "grant"

type creditHistoryRow struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	At          int64  `json:"at"`
	AmountMicro int64  `json:"amount_micro"`
	Kind        string `json:"kind,omitempty"`
	Reference   string `json:"reference,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	TokenPrefix string `json:"token_prefix,omitempty"`
	Seconds     int64  `json:"seconds,omitempty"`
}

func runCreditsHistory(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsHistory.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	limit := fs.Int("limit", 0,
		fmt.Sprintf("maximum rows of each kind, up to %d (0 = the controller's default)", store.CreditHistoryMaxLimit))
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdCreditsHistory, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *limit > store.CreditHistoryMaxLimit {
		return fmt.Errorf("credits history: --limit must not exceed %d rows of each kind",
			store.CreditHistoryMaxLimit)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "credits history"); err != nil {
		return err
	}
	q := url("")
	if *limit > 0 {
		q = q.with("limit", strconv.Itoa(*limit))
	}
	resp, err := tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/history"+q.encode())
	if err != nil {
		return err
	}
	var history creditHistoryResp
	if err := json.Unmarshal(resp, &history); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	rows := creditHistoryRows(history)
	if *outputFormat == "json" {
		return ndjson.Write(os.Stdout, rows)
	}
	return renderCreditHistory(os.Stdout, rows)
}

func creditHistoryRows(history creditHistoryResp) []creditHistoryRow {
	rows := make([]creditHistoryRow, 0, len(history.Grants)+len(history.Charges))
	for _, g := range history.Grants {
		rows = append(rows, creditHistoryRow{
			Type: creditGrantRowType, ID: g.ID, At: g.CreatedAt, AmountMicro: g.AmountMicro,
			Kind: g.Kind, Reference: g.Reference, CreatedBy: g.CreatedBy,
		})
	}
	for _, c := range history.Charges {
		kind := c.Kind
		if kind == "" {
			kind = store.CreditChargeUsage
		}
		// safety: the ledger stores what a charge takes out, so the sign flips
		// here and a refund reads as credits coming back.
		rows = append(rows, creditHistoryRow{
			Type: kind, ID: c.ID, At: c.ChargedAt, AmountMicro: -c.AmountMicro,
			RunID: c.RunID, NodeID: c.NodeID, TokenPrefix: c.TokenPrefix, Seconds: c.Seconds,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At > rows[j].At })
	return rows
}

func renderCreditHistory(w io.Writer, rows []creditHistoryRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "(no credit movements)")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tTYPE\tCREDITS\tDETAIL")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			time.Unix(row.At, 0).UTC().Format("2006-01-02 15:04"),
			row.Type,
			store.FormatCredits(row.AmountMicro),
			creditRowDetail(row))
	}
	return tw.Flush()
}

func creditRowDetail(row creditHistoryRow) string {
	if row.Type == creditGrantRowType {
		detail := row.Kind
		if row.Reference != "" {
			detail += " ref=" + row.Reference
		}
		if row.CreatedBy != "" {
			detail += " by=" + row.CreatedBy
		}
		return detail
	}
	return fmt.Sprintf("%s/%s %ds token=%s", row.RunID, row.NodeID, row.Seconds, row.TokenPrefix)
}
