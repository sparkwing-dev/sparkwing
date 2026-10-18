package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runOperatorSecretsCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect operatorConnector) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path != "sparkwing operator secrets rotate" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	manager, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	result, err := manager.RotateSealedValues(ctx, inv.Bool("verify"))
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	verb := "resealed"
	if inv.Bool("verify") {
		verb = "readable"
	}
	fmt.Fprintf(out, "%s: secrets=%d git_credentials=%d run_environments=%d settings=%d previous_key_only=%d\n", verb, result.Secrets, result.GitCredentials, result.RunEnvironments, result.Settings, result.PreviousKeyOnly)
	for _, row := range result.Unreadable {
		fmt.Fprintf(out, "unreadable: %s team=%s name=%s pipeline=%s\n", row.Purpose, row.Team, row.Name, row.Pipeline)
	}
	if len(result.Unreadable) > 0 || result.PreviousKeyOnly > 0 && inv.Bool("verify") {
		fmt.Fprintln(out, "keep the previous key mounted")
	}
	return nil
}
