package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runOperatorTeams(inv clisurface.Invocation) error {
	return runOperatorTeamsCommand(context.Background(), inv, os.Stdout, resolveOperatorManager)
}

func runOperatorTeamsCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect operatorConnector) error {
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
	if inv.Command.Path == "sparkwing operator teams show" {
		team, err := manager.ReadOperatorTeam(ctx, inv.Value("team"))
		if err != nil {
			return err
		}
		result := struct {
			controllerwire.OperatorTeam
			NodeLabelKey   string `json:"node_label_key"`
			NodeLabelValue string `json:"node_label_value"`
		}{team, launcher.TeamNodeLabel, launcher.TeamNodeValue(team.Team)}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		_, err = fmt.Fprintf(out, "%s\n%s=%s\n", team.Team, result.NodeLabelKey, result.NodeLabelValue)
		return err
	}
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
