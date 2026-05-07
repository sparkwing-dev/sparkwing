// `sparkwing triggers` subcommand. Thin operator surface over the
// controller's /api/v1/triggers endpoints.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

func runTriggers(args []string) error {
	if handleParentHelp(cmdTriggers, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdTriggers, os.Stderr)
		return errors.New("triggers: subcommand required (list|get)")
	}
	switch args[0] {
	case "list":
		return runTriggersList(args[1:])
	case "get":
		return runTriggersGet(args[1:])
	default:
		PrintHelp(cmdTriggers, os.Stderr)
		return fmt.Errorf("triggers: unknown subcommand %q", args[0])
	}
}

// 'sparkwing triggers fire' was a functional duplicate of
// 'sparkwing pipeline run --on <profile>'. Both called CreateTrigger
// on the controller with the same request shape, so the fire
// subcommand was removed. Use 'sparkwing pipeline run --pipeline X
// --on <profile>' instead; pass --from, --config the same way you
// would locally.

// --- list -------------------------------------------------------

func runTriggersList(args []string) error {
	fs := flag.NewFlagSet(cmdTriggersList.Path, flag.ContinueOnError)
	status := fs.String("status", "", "filter by status (pending|claimed|done)")
	pipeline := fs.String("pipeline", "", "filter by pipeline name")
	repo := fs.String("repo", "", "match GITHUB_REPOSITORY on trigger env")
	limit := fs.Int("limit", 20, "max rows")
	quiet := fs.BoolP("quiet", "q", false, "print only trigger ids")
	output := fs.StringP("output", "o", "", "output format (json)")
	asJSON := fs.Bool("json", false, "alias for -o json")
	on := addProfileFlag(fs)

	if err := parseAndCheck(cmdTriggersList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "triggers list"); err != nil {
		return err
	}

	f := store.TriggerFilter{Limit: *limit}
	if *status != "" {
		f.Statuses = []string{*status}
	}
	if *pipeline != "" {
		f.Pipelines = []string{*pipeline}
	}
	if *repo != "" {
		f.Repo = *repo
	}

	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	trigs, err := c.ListTriggers(ctx, f)
	if err != nil {
		return fmt.Errorf("triggers list: %w", err)
	}

	wantJSON := *asJSON || strings.EqualFold(*output, "json")
	if wantJSON {
		buf, _ := json.MarshalIndent(trigs, "", "  ")
		fmt.Fprintln(os.Stdout, string(buf))
		return nil
	}
	if *quiet {
		for _, t := range trigs {
			fmt.Fprintln(os.Stdout, t.ID)
		}
		return nil
	}
	if len(trigs) == 0 {
		fmt.Fprintln(os.Stdout, "(no triggers)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPIPELINE\tSTATUS\tCREATED\tCLAIMED\tBRANCH\tSHA\tREPO")
	for _, t := range trigs {
		created := t.CreatedAt.UTC().Format("2006-01-02 15:04:05")
		claimed := "-"
		if t.ClaimedAt != nil {
			claimed = t.ClaimedAt.UTC().Format("2006-01-02 15:04:05")
		}
		sha := t.GitSHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		repoVal := t.TriggerEnv["GITHUB_REPOSITORY"]
		if repoVal == "" {
			repoVal = "-"
		}
		branch := t.GitBranch
		if branch == "" {
			branch = "-"
		}
		if sha == "" {
			sha = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			t.ID, t.Pipeline, t.Status, created, claimed, branch, sha, repoVal)
	}
	return tw.Flush()
}

// --- get --------------------------------------------------------

func runTriggersGet(args []string) error {
	fs := flag.NewFlagSet(cmdTriggersGet.Path, flag.ContinueOnError)
	id := fs.String("id", "", "trigger identifier")
	output := fs.StringP("output", "o", "", "output format (json)")
	asJSON := fs.Bool("json", false, "alias for -o json")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdTriggersGet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "triggers get"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	trig, err := c.GetTrigger(ctx, *id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("triggers get: %q not found", *id)
		}
		return fmt.Errorf("triggers get: %w", err)
	}

	wantJSON := *asJSON || strings.EqualFold(*output, "json")
	if wantJSON {
		buf, _ := json.MarshalIndent(trig, "", "  ")
		fmt.Fprintln(os.Stdout, string(buf))
		return nil
	}
	// Plain multi-line render. Stable alignment so eyeball diffs work.
	fmt.Fprintf(os.Stdout, "id:         %s\n", trig.ID)
	fmt.Fprintf(os.Stdout, "pipeline:   %s\n", trig.Pipeline)
	fmt.Fprintf(os.Stdout, "status:     %s\n", trig.Status)
	fmt.Fprintf(os.Stdout, "created_at: %s\n", trig.CreatedAt.UTC().Format(time.RFC3339))
	if trig.ClaimedAt != nil {
		fmt.Fprintf(os.Stdout, "claimed_at: %s\n", trig.ClaimedAt.UTC().Format(time.RFC3339))
	}
	if trig.LeaseExpiresAt != nil {
		fmt.Fprintf(os.Stdout, "lease_exp:  %s\n", trig.LeaseExpiresAt.UTC().Format(time.RFC3339))
	}
	if trig.TriggerSource != "" {
		fmt.Fprintf(os.Stdout, "source:     %s\n", trig.TriggerSource)
	}
	if trig.TriggerUser != "" {
		fmt.Fprintf(os.Stdout, "user:       %s\n", trig.TriggerUser)
	}
	if trig.GitBranch != "" {
		fmt.Fprintf(os.Stdout, "git_branch: %s\n", trig.GitBranch)
	}
	if trig.GitSHA != "" {
		fmt.Fprintf(os.Stdout, "git_sha:    %s\n", trig.GitSHA)
	}
	if trig.ParentRunID != "" {
		fmt.Fprintf(os.Stdout, "parent:     %s\n", trig.ParentRunID)
	}
	if len(trig.Args) > 0 {
		fmt.Fprintln(os.Stdout, "args:")
		for k, v := range trig.Args {
			fmt.Fprintf(os.Stdout, "  %s: %s\n", k, v)
		}
	}
	if len(trig.TriggerEnv) > 0 {
		fmt.Fprintln(os.Stdout, "env:")
		for k, v := range trig.TriggerEnv {
			fmt.Fprintf(os.Stdout, "  %s: %s\n", k, v)
		}
	}
	return nil
}
