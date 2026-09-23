package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
		return fmt.Errorf("credits: subcommand required (show|grant|history|settings|allowance)")
	}
	switch args[0] {
	case "show":
		return runCreditsShow(args[1:])
	case "allowance":
		return runCreditsAllowance(args[1:])
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
	BalanceMicro       int64            `json:"balance_micro"`
	GrantedMicro       int64            `json:"granted_micro"`
	ReversedMicro      int64            `json:"reversed_micro"`
	ChargedMicro       int64            `json:"charged_micro"`
	RateMicroPerSecond int64            `json:"rate_micro_per_second"`
	RateTable          []creditRateResp `json:"rate_table"`
	RateTableSet       bool             `json:"rate_table_set"`
	WarmCPUClassCores  int64            `json:"warm_cpu_class_cores"`
	GraceSeconds       int64            `json:"grace_seconds"`
	MaxChargeSeconds   int64            `json:"max_charge_seconds"`

	StorageChargedMicro       int64 `json:"storage_charged_micro"`
	StorageRateMicroPerGBDay  int64 `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64 `json:"storage_free_allowance_bytes"`

	BurnWindowSeconds int64  `json:"burn_window_seconds"`
	BurnMicro         int64  `json:"burn_micro"`
	ExhaustedAt       *int64 `json:"exhausted_at,omitempty"`
	MicroPerCredit    int64  `json:"micro_per_credit"`
	CreditsPerDollar  int64  `json:"credits_per_dollar"`
}

func (s creditStateResp) settings() creditSettingsResp {
	return creditSettingsResp{
		RateMicroPerSecond:        s.RateMicroPerSecond,
		RateTable:                 s.RateTable,
		RateTableSet:              s.RateTableSet,
		WarmCPUClassCores:         s.WarmCPUClassCores,
		GraceSeconds:              s.GraceSeconds,
		MaxChargeSeconds:          s.MaxChargeSeconds,
		StorageRateMicroPerGBDay:  s.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: s.StorageFreeAllowanceBytes,
		MicroPerCredit:            s.MicroPerCredit,
		CreditsPerDollar:          s.CreditsPerDollar,
	}
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
	settings := state.settings()
	fmt.Fprintf(tw, "BALANCE\t%s credits\n", store.FormatCredits(state.BalanceMicro))
	fmt.Fprintf(tw, "GRANTED\t%s credits\n", store.FormatCredits(state.GrantedMicro))
	if state.ReversedMicro != 0 {
		fmt.Fprintf(tw, "REVERSED\t%s credits\n", store.FormatCredits(state.ReversedMicro))
	}
	fmt.Fprintf(tw, "CHARGED\t%s credits\n", store.FormatCredits(state.ChargedMicro))
	writeCreditRateSettings(tw, settings, false)
	fmt.Fprintf(tw, "BURN (%s)\t%s credits\n",
		burnWindowLabel(state.BurnWindowSeconds), store.FormatCredits(state.BurnMicro))
	writeCreditRuntimeSettings(tw, settings)
	if state.StorageChargedMicro != 0 {
		fmt.Fprintf(tw, "STORAGE CHARGED\t%s credits\n", store.FormatCredits(state.StorageChargedMicro))
	}
	fmt.Fprint(tw, storageRateLine(state.StorageRateMicroPerGBDay,
		state.StorageFreeAllowanceBytes, state.MicroPerCredit))
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

// safety: where a class runs decides how fast it starts, so the class the warm
// pool serves is worth a line of its own beside the ladder it prices.
func warmClassLine(cores int64) string {
	if cores <= 0 {
		return "WARM CLASS\tnone; every class starts a node of its own\n"
	}
	return fmt.Sprintf("WARM CLASS\t%d cores; a larger class starts a node of its own\n", cores)
}

// safety: the flat ladder an unset table prints reads like a priced one, so
// the reader is told which of the two it is looking at.
func rateTableOrigin(set bool) string {
	if set {
		return "RATE TABLE\tset by the operator\n"
	}
	return "RATE TABLE\tnot set; the default ladder applies\n"
}

// safety: a sub-credit rate, such as a storage gibibyte-day, is printed to six
// places whatever the controller's micro-credits per credit, so a reader never
// sees a fraction rounded to nothing.
func creditsPerUnit(micro, perCredit int64) string {
	if perCredit <= 0 {
		return strconv.FormatInt(micro, 10)
	}
	whole := micro / perCredit
	frac := micro % perCredit
	if frac < 0 {
		frac = -frac
	}
	return fmt.Sprintf("%d.%06d", whole, frac*1_000_000/perCredit)
}

type creditSettingsResp struct {
	RateMicroPerSecond        int64            `json:"rate_micro_per_second"`
	RateTable                 []creditRateResp `json:"rate_table"`
	RateTableSet              bool             `json:"rate_table_set"`
	WarmCPUClassCores         int64            `json:"warm_cpu_class_cores"`
	GraceSeconds              int64            `json:"grace_seconds"`
	MaxChargeSeconds          int64            `json:"max_charge_seconds"`
	StorageRateMicroPerGBDay  int64            `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64            `json:"storage_free_allowance_bytes"`
	MicroPerCredit            int64            `json:"micro_per_credit"`
	CreditsPerDollar          int64            `json:"credits_per_dollar"`
}

type creditRateResp struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

func runCreditsSettings(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsSettings.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	rate := fs.Int64("rate-micro", 0, "micro-credits one cloud runner second costs")
	grace := fs.Int64("grace-seconds", 0, "seconds a running node survives an empty balance")
	maxCharge := fs.Int64("max-charge-seconds", 0, "the most seconds any one charge may bill")
	rateTable := fs.String("rate-table", "",
		"price every cpu class, as CORES=MICRO pairs, for example 2=10000,4=20000,8=36667")
	warmClass := fs.Int64("warm-cpu-class-cores", store.DefaultWarmCPUClassCores,
		"largest cpu class the warm runner pool serves; a larger class starts a node of its own")
	storageRate := fs.Int64("storage-rate-micro-per-gb-day", 0,
		"micro-credits one gibibyte kept for one day costs; 0 bills no storage")
	storageFree := fs.Int64("storage-free-allowance-bytes", 0,
		"retained bytes every team keeps without being billed for them")
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
	body, err := creditSettingsBody(fs, creditSettingsFlags{
		rate: *rate, grace: *grace, maxCharge: *maxCharge, rateTable: *rateTable,
		warmClass: *warmClass, storageRate: *storageRate, storageFree: *storageFree,
	})
	if err != nil {
		return err
	}
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
// setting. The store owns what each value may be, so the CLI sends what it was
// given and reports the refusal; only the ladder's own spelling is judged here,
// because the wire carries it as a map.
func creditSettingsBody(fs *flag.FlagSet, in creditSettingsFlags) (map[string]any, error) {
	body := map[string]any{}
	if fs.Changed("rate-table") {
		table, err := parseCreditRateTable(in.rateTable)
		if err != nil {
			return nil, err
		}
		body["rate_table"] = table
	}
	for _, named := range []struct {
		flag, field string
		value       int64
	}{
		{"rate-micro", "rate_micro_per_second", in.rate},
		{"warm-cpu-class-cores", "warm_cpu_class_cores", in.warmClass},
		{"grace-seconds", "grace_seconds", in.grace},
		{"max-charge-seconds", "max_charge_seconds", in.maxCharge},
		{"storage-rate-micro-per-gb-day", "storage_rate_micro_per_gb_day", in.storageRate},
		{"storage-free-allowance-bytes", "storage_free_allowance_bytes", in.storageFree},
	} {
		if fs.Changed(named.flag) {
			body[named.field] = named.value
		}
	}
	return body, nil
}

type creditSettingsFlags struct {
	rate        int64
	grace       int64
	maxCharge   int64
	rateTable   string
	warmClass   int64
	storageRate int64
	storageFree int64
}

// safety: an operator writes the ladder as one flag, so the pairs are parsed
// here and sent as the object the settings route accepts.
func parseCreditRateTable(raw string) (map[string]int64, error) {
	table := map[string]int64{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		cores, micro, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("credits settings: --rate-table entry %q must read CORES=MICRO", pair)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(micro), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("credits settings: --rate-table rate %q is not a number", micro)
		}
		key := strings.TrimSpace(cores)
		if _, repeated := table[key]; repeated {
			return nil, fmt.Errorf("credits settings: --rate-table prices %s cores twice", key)
		}
		table[key] = value
	}
	if len(table) == 0 {
		return nil, errors.New("credits settings: --rate-table must price at least one cpu class")
	}
	return table, nil
}

func creditSettingsExchange(prof *profile.Profile, body map[string]any) ([]byte, error) {
	if len(body) == 0 {
		return tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/settings")
	}
	return tokensPut(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/settings", body)
}

func renderCreditSettings(w io.Writer, view creditSettingsResp) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	writeCreditRateSettings(tw, view, true)
	writeCreditRuntimeSettings(tw, view)
	fmt.Fprint(tw, storageRateLine(view.StorageRateMicroPerGBDay,
		view.StorageFreeAllowanceBytes, view.MicroPerCredit))
	return tw.Flush()
}

func writeCreditRateSettings(tw *tabwriter.Writer, view creditSettingsResp, showMicro bool) {
	if showMicro {
		fmt.Fprintf(tw, "RATE\t%s credits per cloud runner second (%d micro)\n",
			creditsPerUnit(view.RateMicroPerSecond, view.MicroPerCredit), view.RateMicroPerSecond)
	} else {
		fmt.Fprintf(tw, "RATE\t%s credits per cloud runner second\n",
			creditsPerUnit(view.RateMicroPerSecond, view.MicroPerCredit))
	}
	for _, entry := range view.RateTable {
		fmt.Fprintf(tw, "  %d-CORE\t%s credits per second (%d micro)\n",
			entry.Cores, creditsPerUnit(entry.MicroPerSecond, view.MicroPerCredit), entry.MicroPerSecond)
	}
	fmt.Fprint(tw, rateTableOrigin(view.RateTableSet))
	fmt.Fprint(tw, warmClassLine(view.WarmCPUClassCores))
}

func writeCreditRuntimeSettings(tw *tabwriter.Writer, view creditSettingsResp) {
	fmt.Fprintf(tw, "GRACE\t%ds past a node's claim reservation\n", view.GraceSeconds)
	fmt.Fprintf(tw, "CHARGE CAP\t%ds billed by any one charge\n", view.MaxChargeSeconds)
}

// safety: a zero rate is the installation that bills no storage at all, which
// reads differently from a rate of zero credits, so it says so.
func storageRateLine(rateMicroPerGBDay, freeBytes, perCredit int64) string {
	if rateMicroPerGBDay <= 0 {
		return "STORAGE RATE\tnone; retained bytes are not billed\n"
	}
	return fmt.Sprintf("STORAGE RATE\t%s credits per gibibyte-day, %d bytes free\n",
		creditsPerUnit(rateMicroPerGBDay, perCredit), freeBytes)
}

func writeCreditSettingsPlain(w io.Writer, view creditSettingsResp) error {
	if _, err := fmt.Fprintf(w, "rate_micro_per_second\t%d\ngrace_seconds\t%d\nmax_charge_seconds\t%d\n",
		view.RateMicroPerSecond, view.GraceSeconds, view.MaxChargeSeconds); err != nil {
		return err
	}
	for _, entry := range view.RateTable {
		if _, err := fmt.Fprintf(w, "rate_micro_per_second.%d\t%d\n",
			entry.Cores, entry.MicroPerSecond); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w,
		"rate_table_set\t%t\nwarm_cpu_class_cores\t%d\n"+
			"storage_rate_micro_per_gb_day\t%d\nstorage_free_allowance_bytes\t%d\n",
		view.RateTableSet, view.WarmCPUClassCores,
		view.StorageRateMicroPerGBDay, view.StorageFreeAllowanceBytes)
	return err
}

type creditGrantResp struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AmountMicro int64  `json:"amount_micro"`
	Reference   string `json:"reference,omitempty"`
	Reverses    string `json:"reverses,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func runCreditsGrant(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsGrant.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	kind := fs.String("kind", "", "grant kind: free|paid|reversal")
	amount := fs.Int64("amount", 0, "credits to add, or to take back as a negative number on a reversal (one credit is a vCPU-second; 20,000 credits = one dollar)")
	reference := fs.String("reference", "", "payment id or operator note recorded with the grant")
	reverses := fs.String("reverses", "", "reference of the paid grant a reversal takes back")
	team := fs.String("team", "", "slug of the team whose balance the grant funds; required on a multi-team controller")
	if err := parseAndCheck(cmdCreditsGrant, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if !store.ValidCreditGrantKind(*kind) {
		return fmt.Errorf("credits grant: --kind must be free, paid or reversal")
	}
	if err := creditGrantAmountRule(*kind, *amount, *reverses); err != nil {
		return err
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
	if *reverses != "" {
		body["reverses"] = *reverses
	}
	if *team != "" {
		body["team"] = *team
	}
	resp, err := tokensPost(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/credits/grants", body)
	if err != nil {
		return err
	}
	var grant creditGrantResp
	if err := json.Unmarshal(resp, &grant); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if grant.Kind == store.CreditGrantReversal {
		fmt.Printf("reversed %s credits of %s as %s\n",
			store.FormatCredits(-grant.AmountMicro), grant.Reverses, grant.ID)
		return nil
	}
	fmt.Printf("granted %s credits (%s) as %s\n",
		store.FormatCredits(grant.AmountMicro), grant.Kind, grant.ID)
	return nil
}

func creditGrantAmountRule(kind string, amount int64, reverses string) error {
	if kind != store.CreditGrantReversal {
		if amount <= 0 {
			return fmt.Errorf("credits grant: --amount must be a positive number of credits")
		}
		if reverses != "" {
			return fmt.Errorf("credits grant: --reverses belongs to a reversal")
		}
		return nil
	}
	if amount >= 0 {
		return fmt.Errorf("credits grant: a reversal takes a negative --amount")
	}
	if reverses == "" {
		return fmt.Errorf("credits grant: a reversal needs --reverses, the reference of the paid grant it takes back")
	}
	return nil
}

type creditChargeResp struct {
	ID                 string `json:"id"`
	RunID              string `json:"run_id"`
	NodeID             string `json:"node_id"`
	TokenPrefix        string `json:"token_prefix"`
	Kind               string `json:"kind"`
	Seconds            int64  `json:"seconds"`
	AmountMicro        int64  `json:"amount_micro"`
	Principal          string `json:"principal,omitempty"`
	StorageBytes       int64  `json:"storage_bytes,omitempty"`
	CPUClassCores      int64  `json:"cpu_class_cores,omitempty"`
	RateMicroPerSecond int64  `json:"rate_micro_per_second,omitempty"`
	ChargedAt          int64  `json:"charged_at"`
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
	Reverses    string `json:"reverses,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	TokenPrefix string `json:"token_prefix,omitempty"`
	Seconds     int64  `json:"seconds,omitempty"`
	// safety: a grant and a charge written before the rate table carry no
	// class, so both fields drop out of the row rather than reading as zero.
	CPUClassCores      int64  `json:"cpu_class_cores,omitempty"`
	RateMicroPerSecond int64  `json:"rate_micro_per_second,omitempty"`
	Principal          string `json:"principal,omitempty"`
	StorageBytes       int64  `json:"storage_bytes,omitempty"`
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
			Kind: g.Kind, Reference: g.Reference, Reverses: g.Reverses, CreatedBy: g.CreatedBy,
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
			CPUClassCores: c.CPUClassCores, RateMicroPerSecond: c.RateMicroPerSecond,
			Principal: c.Principal, StorageBytes: c.StorageBytes,
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
		if row.Reverses != "" {
			detail += " reverses=" + row.Reverses
		}
		if row.CreatedBy != "" {
			detail += " by=" + row.CreatedBy
		}
		return detail
	}
	if row.Type == store.CreditChargeStorage {
		return fmt.Sprintf("team=%s %d bytes retained over %ds",
			row.Principal, row.StorageBytes, row.Seconds)
	}
	detail := fmt.Sprintf("%s/%s %ds token=%s", row.RunID, row.NodeID, row.Seconds, row.TokenPrefix)
	if row.CPUClassCores > 0 {
		detail += fmt.Sprintf(" class=%dc rate=%d", row.CPUClassCores, row.RateMicroPerSecond)
	}
	return detail
}

type storageQuotaResp struct {
	Principal             string `json:"principal"`
	Tier                  string `json:"tier,omitempty"`
	MaxBytesPerRun        int64  `json:"max_bytes_per_run"`
	MaxBytesPerMonth      int64  `json:"max_bytes_per_month"`
	MaxObjectsPerRun      int64  `json:"max_objects_per_run"`
	StorageAllowanceBytes int64  `json:"storage_allowance_bytes"`
}

type storageStateResp struct {
	Quota storageQuotaResp `json:"quota"`
	Usage struct {
		RetainedBytes int64 `json:"retained_bytes"`
	} `json:"usage"`
	Quotas []storageQuotaResp `json:"quotas,omitempty"`
}

func runCreditsAllowance(args []string) error {
	fs := flag.NewFlagSet(cmdCreditsAllowance.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	principal := fs.String("principal", "", "team whose allowance to read or set (default: the calling token's own)")
	gb := fs.Int64("gb", 0, "gibibytes of retained storage to keep; 0 keeps everything")
	bytesFlag := fs.Int64("bytes", 0, "bytes of retained storage to keep; 0 keeps everything")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdCreditsAllowance, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.Changed("gb") && fs.Changed("bytes") {
		return errors.New("credits allowance: name --gb or --bytes, not both")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "credits allowance"); err != nil {
		return err
	}
	if !fs.Changed("gb") && !fs.Changed("bytes") {
		return readStorageAllowance(prof, *principal, *outputFormat)
	}
	if *principal == "" {
		return errors.New("credits allowance: --principal names the team whose allowance to set")
	}
	want := *bytesFlag
	if fs.Changed("gb") {
		if *gb > math.MaxInt64/store.StorageBytesPerGB {
			return fmt.Errorf("credits allowance: --gb must not exceed %d",
				math.MaxInt64/store.StorageBytesPerGB)
		}
		want = *gb * store.StorageBytesPerGB
	}
	if want < 0 {
		return errors.New("credits allowance: the allowance must not be negative")
	}
	resp, err := tokensPut(prof.ControllerURL(), prof.ControllerToken(),
		"/api/v1/storage/quotas/"+*principal+"/allowance",
		map[string]any{"storage_allowance_bytes": want})
	if err != nil {
		return err
	}
	if *outputFormat == "json" {
		_, err := os.Stdout.Write(append(resp, '\n'))
		return err
	}
	var quota storageQuotaResp
	if err := json.Unmarshal(resp, &quota); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	fmt.Printf("team %s keeps %d bytes of retained storage\n",
		quota.Principal, quota.StorageAllowanceBytes)
	return nil
}

func readStorageAllowance(prof *profile.Profile, principal, outputFormat string) error {
	resp, err := tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/storage")
	if err != nil {
		return err
	}
	if outputFormat == "json" {
		_, err := os.Stdout.Write(append(resp, '\n'))
		return err
	}
	var state storageStateResp
	if err := json.Unmarshal(resp, &state); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	quota, retained, err := allowanceForPrincipal(state, principal)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "TEAM\t%s\n", quota.Principal)
	fmt.Fprint(tw, allowanceLine(quota.StorageAllowanceBytes))
	if retained >= 0 {
		fmt.Fprintf(tw, "RETAINED\t%d bytes\n", retained)
	}
	return tw.Flush()
}

// safety: the storage route answers with the calling token's own quota and,
// for an admin, every quota it holds, so another team is read out of that list
// rather than by asking the route for a team it does not take.
func allowanceForPrincipal(state storageStateResp, principal string) (storageQuotaResp, int64, error) {
	if principal == "" || principal == state.Quota.Principal {
		return state.Quota, state.Usage.RetainedBytes, nil
	}
	for _, quota := range state.Quotas {
		if quota.Principal == principal {
			return quota, -1, nil
		}
	}
	return storageQuotaResp{}, 0, fmt.Errorf(
		"credits allowance: this token holds no quota for team %q", principal)
}

// safety: zero is the team that asked for no ceiling at all, which reads
// differently from a ceiling of zero bytes.
func allowanceLine(bytes int64) string {
	if bytes <= 0 {
		return "ALLOWANCE\tnone; every retained byte is kept\n"
	}
	return fmt.Sprintf("ALLOWANCE\t%d bytes; the oldest runs expire above it\n", bytes)
}
