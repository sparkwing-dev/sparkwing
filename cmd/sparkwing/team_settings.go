package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runTeamSettings(ctx context.Context, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"team"}, args...))
	if err != nil {
		return err
	}
	output := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing team" || inv.Command.Path == "sparkwing team settings" {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, output)
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	manager, err := client.NewTeamSettingsClient(options)
	if err != nil {
		return err
	}
	var result controllerwire.TeamSettings
	if inv.Command.Path == "sparkwing team settings set" {
		result, err = manager.SetTeamSettings(ctx, controllerwire.TeamSettings{MembersCanAddMachines: inv.Value("members-can-add-machines") == "true"})
	} else {
		result, err = manager.ReadTeamSettings(ctx)
	}
	if err != nil {
		return err
	}
	if output == "json" {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if output == "plain" {
		fmt.Println(result.MembersCanAddMachines)
		return nil
	}
	fmt.Printf("members_can_add_machines: %t\n", result.MembersCanAddMachines)
	return nil
}
