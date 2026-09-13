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
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runCredits(args []string) error {
	if handleParentHelp(cmdCredits, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCredits, os.Stderr)
		return fmt.Errorf("credits: subcommand required (show|grant|history)")
	}
	switch args[0] {
	case "show":
		return runCreditsShow(args[1:])
	case "grant":
		return runCreditsGrant(args[1:])
	case "history":
		return runCreditsHistory(args[1:])
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
	fmt.Fprintf(tw, "GRACE\t%ds after the balance reaches zero\n", state.GraceSeconds)
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
	Seconds     int64  `json:"seconds"`
	AmountMicro int64  `json:"amount_micro"`
	ChargedAt   int64  `json:"charged_at"`
}

type creditHistoryResp struct {
	Grants  []creditGrantResp  `json:"grants"`
	Charges []creditChargeResp `json:"charges"`
}

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
	limit := fs.Int("limit", 0, "maximum rows of each kind (0 = the controller's default)")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdCreditsHistory, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
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
			Type: "grant", ID: g.ID, At: g.CreatedAt, AmountMicro: g.AmountMicro,
			Kind: g.Kind, Reference: g.Reference, CreatedBy: g.CreatedBy,
		})
	}
	for _, c := range history.Charges {
		rows = append(rows, creditHistoryRow{
			Type: "charge", ID: c.ID, At: c.ChargedAt, AmountMicro: -c.AmountMicro,
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
	if row.Type == "grant" {
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
