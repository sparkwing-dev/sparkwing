package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runOperatorTeams(args []string) error {
	return runOperatorTeamsCommand(context.Background(), args, os.Stdout, resolveOperatorManager)
}

func runOperatorTeamsCommand(ctx context.Context, args []string, out io.Writer, connect operatorConnector) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"operator", "teams"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing operator teams" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if err := store.ValidateSlug(inv.Value("team")); err != nil {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	created, err := manager.CreateOperatorTeam(ctx, controllerwire.OperatorTeamCreate{Slug: inv.Value("team"), DisplayName: inv.Value("display-name")})
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(created)
	}
	_, err = fmt.Fprintln(out, created.Capability)
	return err
}
