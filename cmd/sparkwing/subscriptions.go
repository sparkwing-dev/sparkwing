package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func runSubscriptions(ctx context.Context, inv clisurface.Invocation) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing subscriptions" {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format)
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	manager, err := client.NewSubscriptionManager(options)
	if err != nil {
		return err
	}
	return manageSubscriptions(ctx, inv, manager, os.Stdout, os.Stderr)
}

func manageSubscriptions(ctx context.Context, inv clisurface.Invocation, manager controllerwire.SubscriptionManager, out, errOut io.Writer) error {
	format := inv.Output(color.IsInteractiveStdout())
	switch inv.Command.Path {
	case "sparkwing subscriptions list":
		subs, err := manager.ListSubscriptions(ctx)
		if err != nil {
			return err
		}
		switch format {
		case "json":
			for _, sub := range subs {
				if err := json.NewEncoder(out).Encode(sub); err != nil {
					return err
				}
			}
			return nil
		case "plain":
			for _, sub := range subs {
				if _, err := fmt.Fprintln(out, opsview.DisplayText(sub.ID)); err != nil {
					return err
				}
			}
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tURL\tEVENTS\tSTATUSES\tPIPELINES\tREPOSITORIES")
		for _, sub := range subs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", opsview.DisplayText(sub.ID), opsview.DisplayText(sub.URL), opsview.DisplayText(joinOrDash(sub.EventTypes)),
				opsview.DisplayText(joinOrDash(sub.Statuses)), opsview.DisplayText(joinOrDash(sub.Pipelines)), opsview.DisplayText(joinOrDash(sub.Repositories)))
		}
		return tw.Flush()
	case "sparkwing subscriptions show":
		sub, err := manager.GetSubscription(ctx, inv.Value("subscription"))
		if err != nil {
			return subscriptionLookupError(inv.Value("subscription"), err)
		}
		switch format {
		case "json":
			return json.NewEncoder(out).Encode(sub)
		case "plain":
			_, err = fmt.Fprintln(out, opsview.DisplayText(sub.URL))
			return err
		}
		return writeSubscription(out, sub)
	case "sparkwing subscriptions create":
		result, err := manager.CreateSubscription(ctx, controllerwire.SubscriptionWrite{
			URL: inv.Value("url"), EventTypes: inv.Flags["event"], Statuses: inv.Flags["status"], Pipelines: inv.Flags["pipeline"],
			Repositories: inv.Flags["repo"], Description: inv.Value("description"), IdempotencyKey: rand.Text(),
		})
		if err != nil {
			return err
		}
		return writeSubscriptionSecret(out, errOut, format, result, true)
	case "sparkwing subscriptions rotate-secret":
		result, err := manager.RotateSubscriptionSecret(ctx, inv.Value("subscription"))
		if err != nil {
			return subscriptionLookupError(inv.Value("subscription"), err)
		}
		return writeSubscriptionSecret(out, errOut, format, result, false)
	case "sparkwing subscriptions delete":
		result, err := manager.DeleteSubscription(ctx, inv.Value("subscription"))
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		if !result.Deleted {
			return subscriptionNotFound(result.ID)
		}
		_, err = fmt.Fprintln(out, opsview.DisplayText(result.ID))
		return err
	}
	return &clisurface.UsageError{Path: inv.Command.Path, Message: "unknown subscriptions command"}
}

func subscriptionLookupError(id string, err error) error {
	var failure *apierr.Error
	if errors.As(err, &failure) && failure.Code == apierr.NotFound {
		return subscriptionNotFound(id)
	}
	return err
}

func subscriptionNotFound(id string) error {
	return &apierr.Error{
		Code: apierr.NotFound, Message: fmt.Sprintf("subscription %s was not found in this team", id),
		Hint: "run `sparkwing subscriptions list` to see this team's subscription IDs",
	}
}

func writeSubscription(out io.Writer, sub controllerwire.Subscription) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"ID", sub.ID},
		{"URL", sub.URL},
		{"Events", joinOrDash(sub.EventTypes)},
		{"Statuses", joinOrDash(sub.Statuses)},
		{"Pipelines", joinOrDash(sub.Pipelines)},
		{"Repositories", joinOrDash(sub.Repositories)},
		{"Description", dashIfEmpty(sub.Description)},
	} {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], opsview.DisplayText(row[1]))
	}
	return tw.Flush()
}

func writeSubscriptionSecret(out, errOut io.Writer, format string, result controllerwire.SubscriptionSecret, withID bool) error {
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(result)
	case "plain":
		lines := []string{result.Secret}
		if withID {
			lines = []string{result.Subscription.ID, result.Secret}
		}
		_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
		return err
	}
	if err := writeSubscription(out, result.Subscription); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Secret:  %s\n", result.Secret); err != nil {
		return err
	}
	_, err := fmt.Fprintln(errOut, "Store this signing secret now; Sparkwing will not show it again.")
	return err
}
