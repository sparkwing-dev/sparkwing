package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runPools(ctx context.Context, inv clisurface.Invocation) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing pools" {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format)
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	manager, err := client.NewPoolManager(options)
	if err != nil {
		return err
	}
	return managePools(ctx, inv, manager, os.Stdout)
}

func managePools(ctx context.Context, inv clisurface.Invocation, manager controllerwire.PoolManager, out io.Writer) error {
	format := inv.Output(color.IsInteractiveStdout())
	switch inv.Command.Path {
	case "sparkwing pools list":
		pools, err := manager.ListPools(ctx)
		if err != nil {
			return err
		}
		if format == "json" {
			for _, pool := range pools.Pools {
				if err := json.NewEncoder(out).Encode(pool); err != nil {
					return err
				}
			}
			return nil
		}
		if format == "plain" {
			for _, pool := range pools.Pools {
				if _, err := fmt.Fprintln(out, pool.Name); err != nil {
					return err
				}
			}
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "POOL\tRUNNERS\tRULE\tREPOSITORIES\tBRANCHES\tSOURCE\tROUTE")
		for _, pool := range pools.Pools {
			if len(pool.Rules) == 0 {
				fmt.Fprintf(tw, "%s\t%s\t-\t-\t-\t-\t-\n", pool.Name, joinOrDash(pool.Runners))
				continue
			}
			for _, rule := range pool.Rules {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%t\n", pool.Name, joinOrDash(pool.Runners), rule.ID, joinOrDash(rule.Repos), joinOrDash(rule.Branches), dashIfEmpty(rule.Source), rule.Route)
			}
		}
		return tw.Flush()
	case "sparkwing pools allow":
		result, err := manager.AllowPool(ctx, inv.Value("pool"), controllerwire.PoolRuleWrite{Repos: inv.Flags["repo"], Branches: inv.Flags["branch"], Source: inv.Value("source"), Route: inv.Bool("route"), IdempotencyKey: rand.Text()})
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		_, err = fmt.Fprintln(out, result.Rule.ID)
		return err
	case "sparkwing pools disallow":
		result, err := manager.DisallowPool(ctx, inv.Value("pool"), inv.Value("rule"))
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		_, err = fmt.Fprintln(out, result.RuleID)
		return err
	}
	return &clisurface.UsageError{Path: inv.Command.Path, Message: "unknown pool command"}
}
