package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runBilling(ctx context.Context, inv clisurface.Invocation) error {
	return runBillingCommand(ctx, inv, os.Stdout, func(ctx context.Context, profile string) (controllerwire.BillingManager, func(), error) {
		options, closeTransport, err := controllerOptions(ctx, profile)
		if err != nil {
			return nil, nil, err
		}
		manager, err := client.NewBillingManager(options)
		if err != nil {
			closeTransport()
			return nil, nil, err
		}
		return manager, closeTransport, nil
	})
}

func runBillingCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect func(context.Context, string) (controllerwire.BillingManager, func(), error)) error {
	if inv.Help() || inv.Command.Path == "sparkwing billing" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(color.IsInteractiveStdout()))
	}
	manager, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	return manageBilling(ctx, inv, manager, out)
}

func manageBilling(ctx context.Context, inv clisurface.Invocation, manager controllerwire.BillingManager, out io.Writer) error {
	format := inv.Output(color.IsInteractiveStdout())
	switch inv.Command.Path {
	case "sparkwing billing standing":
		standing, err := manager.ReadStanding(ctx)
		if err != nil {
			return err
		}
		return writeStanding(out, format, standing)
	case "sparkwing billing request-tier":
		made, err := manager.RequestStanding(ctx, controllerwire.StandingRequestWrite{Tier: inv.Value("tier"), Message: inv.Value("message")})
		if err != nil {
			return err
		}
		switch format {
		case "json":
			return json.NewEncoder(out).Encode(made)
		case "plain":
			_, err = fmt.Fprintln(out, made.ID)
			return err
		}
		_, err = fmt.Fprintf(out, "Sent your request for %s standing, higher limits or larger payments (%s). An operator will review your message; check with sparkwing billing standing.\n", made.Tier, made.ID)
		return err
	case "sparkwing billing refill":
		return manageRefill(ctx, inv, manager, out, format)
	}
	return &clisurface.UsageError{Path: inv.Command.Path, Message: "unknown billing command"}
}

func writeStanding(out io.Writer, format string, s controllerwire.TeamStanding) error {
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(s)
	case "plain":
		_, err := fmt.Fprintln(out, s.Tier)
		return err
	}
	if _, err := fmt.Fprintf(out, "Standing: %s\nHistory tier: %s\nPrepaid funds can reach: %s\n", s.Tier, s.History.Tier, s.PrepayCeiling); err != nil {
		return err
	}
	if r := s.Request; r != nil {
		line := fmt.Sprintf("Request: %s %s, sent %s", r.Tier, r.State, time.Unix(r.RequestedAt, 0).UTC().Format(time.RFC3339))
		if r.DecisionReason != "" {
			line += " (" + r.DecisionReason + ")"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

var refillChangeFlags = []string{"enable", "disable", "threshold", "amount", "week-limit", "month-limit"}

// safety: only the flags given change the current settings, so changing the amount never turns refill on or clears
// a limit by accident.
func manageRefill(ctx context.Context, inv clisurface.Invocation, manager controllerwire.BillingManager, out io.Writer, format string) error {
	refill, err := manager.ReadRefill(ctx)
	if err != nil {
		return err
	}
	changed := false
	for _, name := range refillChangeFlags {
		changed = changed || inv.Has(name)
	}
	if changed {
		w := controllerwire.RefillWrite{
			Enabled: refill.Enabled, ThresholdCents: refill.ThresholdCents, AmountCents: refill.AmountCents,
			WeekLimitCents: refill.WeekLimitCents, MonthLimitCents: refill.MonthLimitCents,
		}
		if inv.Has("enable") {
			w.Enabled = true
		}
		if inv.Has("disable") {
			w.Enabled = false
		}
		for name, field := range map[string]*int64{"threshold": &w.ThresholdCents, "amount": &w.AmountCents, "week-limit": &w.WeekLimitCents, "month-limit": &w.MonthLimitCents} {
			if !inv.Has(name) {
				continue
			}
			cents, ok := parseDollarCents(inv.Value(name))
			if !ok {
				return &clisurface.UsageError{Path: inv.Command.Path, Message: fmt.Sprintf("--%s takes a dollar amount such as 25 or 12.50", name)}
			}
			*field = cents
		}
		if err := manager.SetRefill(ctx, w); err != nil {
			return err
		}
		if refill, err = manager.ReadRefill(ctx); err != nil {
			return err
		}
	}
	return writeRefill(out, format, refill)
}

var dollarPattern = regexp.MustCompile(`^\$?(\d{1,9})(?:\.(\d{1,2}))?$`)

func parseDollarCents(s string) (int64, bool) {
	m := dollarPattern.FindStringSubmatch(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if m == nil {
		return 0, false
	}
	dollars, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	cents, err := strconv.ParseInt(m[2]+strings.Repeat("0", 2-len(m[2])), 10, 64)
	return dollars*100 + cents, err == nil
}

func fmtDollars(cents int64) string {
	if cents%100 == 0 {
		return fmt.Sprintf("$%d", cents/100)
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

var refillLimitNames = map[string]string{
	"payment_size":    "the maximum per payment; contact us through sparkwing billing request-tier for larger payments",
	"standing_size":   "the standing's per-refill cap",
	"standing_daily":  "the standing's refills per day",
	"standing_weekly": "the standing's weekly cap",
	"team_weekly":     "the team's weekly limit",
	"team_monthly":    "the team's monthly limit",
}

func writeRefill(out io.Writer, format string, r controllerwire.TeamRefill) error {
	state := map[bool]string{true: "on", false: "off"}[r.Enabled]
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(r)
	case "plain":
		_, err := fmt.Fprintln(out, state)
		return err
	}
	limit := func(cents int64) string {
		if cents == 0 {
			return "none"
		}
		return fmtDollars(cents)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Automatic refill: %s\n", state)
	fmt.Fprintf(&b, "Threshold: %s\nAmount: %s (%s to %s)\n", fmtDollars(r.ThresholdCents), fmtDollars(r.AmountCents), fmtDollars(r.MinAmountCents), fmtDollars(r.MaxAmountCents))
	fmt.Fprintf(&b, "Team limits: %s a week, %s a month\n", limit(r.WeekLimitCents), limit(r.MonthLimitCents))
	if r.Caps == (controllerwire.RefillCaps{}) {
		b.WriteString("Standing refill caps: none; maximum per payment and team limits apply\n")
	} else {
		fmt.Fprintf(&b, "Refill caps: %s per refill, %d a day, %s a week\n", fmtDollars(r.Caps.AmountCents), r.Caps.PerDay, fmtDollars(r.Caps.WeekCents))
	}
	fmt.Fprintf(&b, "Refilled: %s in the last 7 days, %s in the last 30 days\n", fmtDollars(r.WeekCents), fmtDollars(r.MonthCents))
	if r.Open {
		b.WriteString("A refill is in progress.\n")
	}
	if r.FailedAt > 0 {
		fmt.Fprintf(&b, "The last refill failed at %s", time.Unix(r.FailedAt, 0).UTC().Format(time.RFC3339))
		if r.Failure != "" {
			fmt.Fprintf(&b, " (%s)", r.Failure)
		}
		b.WriteString(". Save a card or add funds to start refills again.\n")
	}
	if r.Enabled && r.Limit != "" {
		name := refillLimitNames[r.Limit]
		if name == "" {
			name = r.Limit
		}
		fmt.Fprintf(&b, "The next refill is stopped by %s; Cloud work stops when the credits run out.\n", name)
	}
	_, err := io.WriteString(out, b.String())
	return err
}
