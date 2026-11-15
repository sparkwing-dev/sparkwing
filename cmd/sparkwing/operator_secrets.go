package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"

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
	fmt.Fprintf(out, "%s: secrets=%d git_credentials=%d run_environments=%d run_arguments=%d settings=%d run_event_subscriptions=%d previous_key_only=%d plaintext_run_arguments=%d\n", verb, result.Secrets, result.GitCredentials, result.RunEnvironments, result.RunArguments, result.Settings, result.RunEventSubscriptions, result.PreviousKeyOnly, result.PlaintextRunArguments)
	for _, row := range result.Unreadable {
		fmt.Fprintf(out, "unreadable: %s team=%s name=%s pipeline=%s\n", opsview.DisplayText(row.Purpose), opsview.DisplayText(row.Team), opsview.DisplayText(row.Name), opsview.DisplayText(row.Pipeline))
	}
	if len(result.Unreadable) > 0 || result.PreviousKeyOnly > 0 && inv.Bool("verify") {
		fmt.Fprintln(out, "keep the previous key mounted")
	}
	if result.PlaintextRunArguments > 0 && inv.Bool("verify") {
		fmt.Fprintln(out, "run the rotation without --verify to seal run arguments stored in plaintext")
	}
	return nil
}
