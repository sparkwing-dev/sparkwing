package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runTeamDelete(ctx context.Context, inv clisurface.Invocation) error {
	return runTeamDeleteCommand(ctx, inv, os.Stdout, func(ctx context.Context, profile string) (controllerwire.TeamDeleter, func(), error) {
		options, closeTransport, err := controllerOptions(ctx, profile)
		if err != nil {
			return nil, nil, err
		}
		deleter, err := client.NewTeamDeleter(options)
		if err != nil {
			closeTransport()
			return nil, nil, err
		}
		return deleter, closeTransport, nil
	})
}

func runTeamDeleteCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect func(context.Context, string) (controllerwire.TeamDeleter, func(), error)) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(color.IsInteractiveStdout()))
	}
	deleter, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	deletion, err := deleter.DeleteTeam(ctx, inv.Value("confirm"))
	if err != nil {
		return err
	}
	switch inv.Output(color.IsInteractiveStdout()) {
	case "json":
		return json.NewEncoder(out).Encode(deletion)
	case "plain":
		_, err = fmt.Fprintln(out, deletion.Slug)
		return err
	}
	_, err = fmt.Fprintf(out, "Team %s is being deleted (%s). The controller removes its data within a few minutes.\n"+
		"This profile's token is revoked; remove the profile with sparkwing profiles remove --profile %s --keep-token.\n",
		deletion.Slug, deletion.State, inv.Value("profile"))
	return err
}
