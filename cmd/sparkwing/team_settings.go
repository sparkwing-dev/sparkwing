package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
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
		fmt.Printf("%t %s %d\n", result.MembersCanAddMachines, ranking, result.MaxChildrenPerRoot)
	default:
		fmt.Printf("members_can_add_machines: %t\nclaim_ranking: %s\nmax_children_per_root: %d\n", result.MembersCanAddMachines, ranking, result.MaxChildrenPerRoot)
	}
	return nil
}

func teamSettingsUpdate(inv clisurface.Invocation) (controllerwire.TeamSettingsUpdate, error) {
	var update controllerwire.TeamSettingsUpdate
	if inv.Has("max-children-per-root") {
		ceiling, err := strconv.ParseInt(inv.Value("max-children-per-root"), 10, 64)
		if err != nil || ceiling <= 0 || ceiling > limits.MaxChildrenPerRoot {
			return update, &clisurface.UsageError{Path: inv.Command.Path, Message: "--max-children-per-root must be an integer from 1 to 1000"}
		}
		update.MaxChildrenPerRoot = &ceiling
	}
	if raw := inv.Value("members-can-add-machines"); raw != "" {
		enabled := raw == "true"
		update.MembersCanAddMachines = &enabled
	}
	if inv.Has("claim-ranking") {
		var tiers []string
		switch value := strings.TrimSpace(inv.Value("claim-ranking")); value {
		case "none":
			tiers = []string{}
		case store.ClaimRankingDefault:
			tiers = []string{store.ClaimRankingDefault}
		default:
			for _, tier := range strings.Split(value, ",") {
				tiers = append(tiers, strings.TrimSpace(tier))
			}
		}
		update.ClaimRanking = &tiers
	}
	if update.MembersCanAddMachines == nil && update.ClaimRanking == nil && update.MaxChildrenPerRoot == nil {
		return update, &clisurface.UsageError{Path: inv.Command.Path, Message: "pass --members-can-add-machines, --claim-ranking or --max-children-per-root"}
	}
	return update, nil
}
