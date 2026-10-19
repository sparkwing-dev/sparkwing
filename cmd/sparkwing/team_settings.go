package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runTeamSettings(ctx context.Context, inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
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
		update, err := teamSettingsUpdate(inv)
		if err != nil {
			return err
		}
		result, err = manager.SetTeamSettings(ctx, update)
		if err != nil {
			return err
		}
	} else if result, err = manager.ReadTeamSettings(ctx); err != nil {
		return err
	}
	ranking := strings.Join(result.ClaimRanking, ",")
	if ranking == "" {
		ranking = "none"
	}
	switch output {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "plain":
		fmt.Printf("%t %s\n", result.MembersCanAddMachines, ranking)
	default:
		fmt.Printf("members_can_add_machines: %t\nclaim_ranking: %s\n", result.MembersCanAddMachines, ranking)
	}
	return nil
}

func teamSettingsUpdate(inv clisurface.Invocation) (controllerwire.TeamSettingsUpdate, error) {
	var update controllerwire.TeamSettingsUpdate
	if raw := inv.Value("members-can-add-machines"); raw != "" {
		enabled := raw == "true"
		update.MembersCanAddMachines = &enabled
	}
	if inv.Has("claim-ranking") {
		var tiers []string
		switch value := strings.TrimSpace(inv.Value("claim-ranking")); value {
		case "none":
			tiers = []string{}
		case "default":
			tiers = slices.Clone(store.DefaultClaimRanking)
		default:
			for _, tier := range strings.Split(value, ",") {
				tiers = append(tiers, strings.TrimSpace(tier))
			}
		}
		update.ClaimRanking = &tiers
	}
	if update.MembersCanAddMachines == nil && update.ClaimRanking == nil {
		return update, &clisurface.UsageError{Path: inv.Command.Path, Message: "pass --members-can-add-machines or --claim-ranking"}
	}
	return update, nil
}

func runMachineAutomation(ctx context.Context, inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	manager, err := client.NewTeamSettingsClient(options)
	if err != nil {
		return err
	}
	result, err := manager.SetMachineAutomation(ctx, inv.Value("runner"), inv.Value("automated-runs") == "true")
	if err != nil {
		return err
	}
	switch output {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "plain":
		fmt.Println(result.AutomatedRuns)
	default:
		fmt.Printf("%s automated_runs: %t\n", result.Runner, result.AutomatedRuns)
	}
	return nil
}
