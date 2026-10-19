package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runBilling(ctx context.Context, inv clisurface.Invocation) error {
	return runBillingCommand(ctx, inv, os.Stdout, func(ctx context.Context, profile string) (controllerwire.StandingManager, func(), error) {
		options, closeTransport, err := controllerOptions(ctx, profile)
		if err != nil {
			return nil, nil, err
		}
		manager, err := client.NewStandingManager(options)
		if err != nil {
			closeTransport()
			return nil, nil, err
		}
		return manager, closeTransport, nil
	})
}

func runBillingCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect func(context.Context, string) (controllerwire.StandingManager, func(), error)) error {
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

func manageBilling(ctx context.Context, inv clisurface.Invocation, manager controllerwire.StandingManager, out io.Writer) error {
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
		_, err = fmt.Fprintf(out, "Requested %s standing (%s). An operator approves by granting standing or declines with a reason; check with sparkwing billing standing.\n", made.Tier, made.ID)
		return err
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
