package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

var allCommands = []*Command{
	&cmdSparkwing, &cmdInfo, &cmdCluster, &cmdCommands, &cmdQueue, &cmdQueueExec, &cmdDaemon, &cmdDaemonStatus, &cmdDaemonRestart, &cmdDaemonRecoverState, &cmdUpdate, &cmdVersion, &cmdVersionUpdate, &cmdVersionHold, &cmdRun, &cmdRunConfig,
	&cmdConfigure, &cmdConfigureInit, &cmdConfigureXrepo, &cmdConfigureXrepoList, &cmdConfigureXrepoAdd, &cmdConfigureXrepoRemove, &cmdConfigureXrepoPrune,
	&cmdDocs, &cmdDocsList, &cmdDocsRead, &cmdDocsGuides, &cmdDocsAll, &cmdDocsSearch,
	&cmdDocsMigrations, &cmdDocsMigrationsList, &cmdDocsMigrationsRead, &cmdDocsMigrationsBetween,
	&cmdDocsVersions, &cmdDocsCache, &cmdDocsCacheInfo, &cmdDocsCacheClear,
	&cmdCache, &cmdCacheInfo, &cmdCachePrune, &cmdCacheExplain,
	&cmdDebug, &cmdDebugRun, &cmdDebugRelease, &cmdDebugAttach,
	&cmdDebugRerun, &cmdDebugReplay, &cmdDebugEnv,
	&cmdPipeline, &cmdPipelineList, &cmdPipelineDescribe, &cmdPipelineDiscover,
	&cmdPipelineNew, &cmdExamples, &cmdExampleScaffold, &cmdPipelineExplain, &cmdPipelineLint, &cmdPipelinePlan, &cmdPipelineRun,
	&cmdPipelineTrigger,
	&cmdProfile,
	&cmdDashboard, &cmdDashboardStart, &cmdDashboardKill, &cmdDashboardStatus,
	&cmdWorker, &cmdGC, &cmdCompletion, &cmdDoctor,
	&cmdProfiles, &cmdProfilesAdd, &cmdProfilesList, &cmdProfilesShow,
	&cmdProfilesRemove, &cmdProfilesDuplicate,
	&cmdProfilesSet, &cmdProfilesTest,
	&cmdTokens, &cmdTokensCreate, &cmdTokensList, &cmdTokensRevoke,
	&cmdTokensLookup, &cmdTokensRotate,
	&cmdUsers, &cmdUsersAdd, &cmdUsersList, &cmdUsersDelete,
	&cmdJobs, &cmdJobsList, &cmdJobsStatus, &cmdJobsLogs, &cmdJobsErrors,
	&cmdJobsFailures, &cmdJobsStats, &cmdJobsLast, &cmdJobsTree,
	&cmdJobsGet, &cmdJobsReceipt, &cmdJobsWait, &cmdJobsFind, &cmdJobsRetry,
	&cmdJobsCancel, &cmdJobsBounce, &cmdJobsPrune, &cmdJobsTimeline, &cmdJobsSummary, &cmdJobsGrep,
	&cmdJobsSubmit, &cmdJobsConsumer, &cmdJobsConsumerStart, &cmdJobsConsumerStatus, &cmdJobsConsumerStop,
	&cmdHooks, &cmdHooksInstall, &cmdHooksUninstall, &cmdHooksStatus, &cmdHooksSurvey, &cmdHooksFire,
	&cmdSecret, &cmdSecretSet, &cmdSecretGet, &cmdSecretList, &cmdSecretDelete,
	&cmdTriggers, &cmdTriggersList, &cmdTriggersGet,
	&cmdImage, &cmdImageRollout,
	&cmdHealth,
	&cmdWebhooks, &cmdWebhooksList, &cmdWebhooksDeliveries, &cmdWebhooksReplay,
	&cmdAgents, &cmdAgentsList, &cmdAgentsEnroll, &cmdClusterConcurrency,
	&cmdSparks, &cmdSparksList, &cmdSparksLint, &cmdSparksResolve,
	&cmdSparksUpdate, &cmdSparksAdd, &cmdSparksRemove, &cmdSparksWarmup, &cmdSparksInflate,
	&cmdApprove, &cmdDeny, &cmdApprovals, &cmdApprovalsList,
	&cmdAnnotations, &cmdAnnotationsList, &cmdAnnotationsAdd,
	&cmdRepos, &cmdReposList, &cmdReposInfo, &cmdReposUpdate,
}

type CommandIndexJSON struct {
	Path            string `json:"path"`
	Synopsis        string `json:"synopsis"`
	SubcommandCount int    `json:"subcommand_count"`

	Hidden bool `json:"hidden,omitempty"`
}

type CommandJSON struct {
	Path        string           `json:"path"`
	Synopsis    string           `json:"synopsis"`
	Description string           `json:"description,omitempty"`
	Hidden      bool             `json:"hidden,omitempty"`
	Subcommands []SubcommandJSON `json:"subcommands,omitempty"`
	PosArgs     []PosArgJSON     `json:"positional_args,omitempty"`
	Flags       []FlagJSON       `json:"flags,omitempty"`
	Examples    []ExampleJSON    `json:"examples,omitempty"`
}

type SubcommandJSON struct {
	Name     string `json:"name"`
	Synopsis string `json:"synopsis"`
}

type PosArgJSON struct {
	Name     string `json:"name"`
	Desc     string `json:"description"`
	Required bool   `json:"required"`
}

type FlagJSON struct {
	Name          string   `json:"name"`
	Short         string   `json:"short,omitempty"`
	Argument      string   `json:"argument,omitempty"`
	Desc          string   `json:"description"`
	Group         string   `json:"group,omitempty"`
	Default       string   `json:"default,omitempty"`
	Required      bool     `json:"required,omitempty"`
	RequiredWhen  string   `json:"required_when,omitempty"`
	RequiresFlags []string `json:"requires_flags,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	Hidden        bool     `json:"hidden,omitempty"`
}

type ExampleJSON struct {
	Description string `json:"description"`
	Command     string `json:"command"`
}

func toCommandJSON(c *Command) CommandJSON {
	out := CommandJSON{
		Path:        c.Path,
		Synopsis:    c.Synopsis,
		Description: c.Description,
		Hidden:      c.Hidden,
	}

	for _, s := range visibleSubcommands(*c) {
		out.Subcommands = append(out.Subcommands, SubcommandJSON(s))
	}
	for _, p := range c.PosArgs {
		out.PosArgs = append(out.PosArgs, PosArgJSON(p))
	}
	for _, f := range c.Flags {
		if f.Hidden {
			continue
		}
		out.Flags = append(out.Flags, FlagJSON{
			Name:          f.Name,
			Short:         f.Short,
			Argument:      f.Argument,
			Desc:          f.Desc,
			Group:         f.Group,
			Default:       f.Default,
			Required:      f.Required,
			RequiredWhen:  f.RequiredWhen,
			RequiresFlags: f.RequiresFlags,
			ConflictsWith: f.ConflictsWith,
		})
	}
	for _, e := range c.Examples {
		out.Examples = append(out.Examples, ExampleJSON{Description: e.Desc, Command: e.Command})
	}
	return out
}

func commandIndex(picked []*Command) []CommandIndexJSON {
	children := make(map[string]int, len(picked))
	for _, c := range picked {
		if i := strings.LastIndex(c.Path, " "); i >= 0 {
			children[c.Path[:i]]++
		}
	}
	out := make([]CommandIndexJSON, 0, len(picked))
	for _, c := range picked {
		out = append(out, CommandIndexJSON{
			Path:            c.Path,
			Synopsis:        c.Synopsis,
			SubcommandCount: children[c.Path],
			Hidden:          c.Hidden,
		})
	}
	return out
}

func runCommands(args []string) error {
	fs := flag.NewFlagSet(cmdCommands.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | markdown | plain")
	includeHidden := fs.Bool("include-hidden", false, "also emit Hidden:true commands (default: skip)")
	pathPrefix := fs.String("path", "", "only emit commands whose Path starts with this prefix")
	splitDir := fs.String("split-dir", "", "with -o markdown: write one page per top-level command group into this directory")
	if err := parseAndCheck(cmdCommands, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("commands: unexpected positional %q", fs.Arg(0))
	}
	if *splitDir != "" {
		if o := strings.ToLower(output); o != "markdown" && o != "md" {
			return fmt.Errorf("commands: --split-dir requires -o markdown")
		}

		if *pathPrefix != "" {
			return fmt.Errorf("commands: --split-dir writes the full reference and conflicts with --path")
		}
	}

	sorted := make([]*Command, len(allCommands))
	copy(sorted, allCommands)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	prefix := strings.TrimSpace(*pathPrefix)
	if blankPathFilter(*pathPrefix, prefix) {
		return unmatchedPathError(*pathPrefix, 0)
	}
	picked := []*Command{}
	hiddenMatches := 0
	for _, c := range sorted {
		if !matchesCommandPath(c.Path, prefix) {
			continue
		}

		if c.Hidden && !*includeHidden {
			hiddenMatches++
			continue
		}
		picked = append(picked, c)
	}
	if len(picked) == 0 && prefix != "" {
		return unmatchedPathError(prefix, hiddenMatches)
	}

	switch strings.ToLower(output) {
	case "json":

		enc := json.NewEncoder(os.Stdout)
		for _, c := range commandIndex(picked) {
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		return nil
	case "markdown", "md":
		full := make([]CommandJSON, 0, len(picked))
		for _, c := range picked {
			full = append(full, toCommandJSON(c))
		}
		if *splitDir != "" {
			return writeSplitMarkdown(*splitDir, full)
		}
		fmt.Print(renderCommandsMarkdown(full))
		return nil
	case "plain":
		for _, c := range picked {
			fmt.Println(c.Path)
		}
		return nil
	case "pretty", "table", "":
		w := 0
		for _, c := range picked {
			if n := len(c.Path); n > w {
				w = n
			}
		}
		fmt.Printf("%s  %s\n",
			color.Bold(fmt.Sprintf("%-*s", w, "PATH")),
			color.Bold("SYNOPSIS"))
		for _, c := range picked {
			fmt.Printf("%-*s  %s\n", w, c.Path, color.Dim(c.Synopsis))
		}

		fmt.Println()
		printAlignedSteps([]InfoNextStep{
			{Command: "<any path above> --help", Purpose: "flags, arguments, examples for one verb"},
			{Command: "sparkwing commands --path pipeline -o json", Purpose: "this index as JSON, one record per line"},
		})
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, markdown, plain)", output)
	}
}

func matchesCommandPath(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	return hasPathComponentPrefix(path, prefix) ||
		hasPathComponentPrefix(path, cmdSparkwing.Path+" "+prefix)
}

func hasPathComponentPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+" ")
}

func blankPathFilter(raw, trimmed string) bool { return raw != "" && trimmed == "" }

func unmatchedPathError(prefix string, hiddenMatches int) error {
	if hiddenMatches > 0 {
		return fmt.Errorf("commands: --path %q matched only hidden commands; pass --include-hidden to list them", prefix)
	}
	return fmt.Errorf("commands: --path %q matched no command; `sparkwing commands -o plain` lists every path", prefix)
}

func renderCommandsMarkdown(cmds []CommandJSON) string {
	var b strings.Builder
	b.WriteString(generatedPageMarker)
	b.WriteString("# CLI reference\n\n")
	b.WriteString("Complete listing of every `sparkwing` command, flag, and argument, " +
		"generated from the CLI's own command registry. For the conceptual " +
		"overview -- which binaries exist, the flag-naming rule, and what to " +
		"reach for when -- see [cli.md](cli.md).\n\n")
	for _, c := range cmds {
		writeCommandSection(&b, c, true)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

const generatedPageMarker = "<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->\n" +
	"<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->\n"

func writeCommandSection(b *strings.Builder, c CommandJSON, withSubcommands bool) {
	{
		b.WriteString("## `" + c.Path + "`\n\n")
		if s := strings.TrimSpace(c.Synopsis); s != "" {
			b.WriteString(s + "\n\n")
		}
		if d := strings.TrimSpace(c.Description); d != "" {
			b.WriteString(descBlock(d) + "\n\n")
		}
		if withSubcommands && len(c.Subcommands) > 0 {
			b.WriteString("### Subcommands\n\n")
			for _, s := range c.Subcommands {
				b.WriteString("- `" + s.Name + "` -- " + cell(s.Synopsis) + "\n")
			}
			b.WriteString("\n")
		}
		if len(c.PosArgs) > 0 {
			b.WriteString("### Arguments\n\n")
			for _, p := range c.PosArgs {
				req := "optional"
				if p.Required {
					req = "required"
				}
				b.WriteString("- `" + p.Name + "` (" + req + ") -- " + cell(p.Desc) + "\n")
			}
			b.WriteString("\n")
		}
		if len(c.Flags) > 0 {
			b.WriteString("### Flags\n\n")
			b.WriteString("| Flag | Description |\n|---|---|\n")
			for _, f := range c.Flags {
				name := "--" + f.Name
				if f.Short != "" {
					name = "-" + f.Short + ", --" + f.Name
				}
				if f.Argument != "" {
					name += " " + f.Argument
				}
				desc := f.Desc
				var extra []string
				if f.Required {
					extra = append(extra, "required")
				}
				if f.Default != "" {
					extra = append(extra, "default: "+f.Default)
				}
				if len(extra) > 0 {
					desc += " (" + strings.Join(extra, "; ") + ")"
				}
				b.WriteString("| `" + name + "` | " + cell(desc) + " |\n")
			}
			b.WriteString("\n")
		}
		if len(c.Examples) > 0 {
			b.WriteString("### Examples\n\n```sh\n")
			for i, e := range c.Examples {
				if i > 0 {
					b.WriteString("\n")
				}
				if e.Description != "" {
					b.WriteString("# " + e.Description + "\n")
				}
				b.WriteString(e.Command + "\n")
			}
			b.WriteString("```\n\n")
		}
	}
}

func splitCommandsMarkdown(cmds []CommandJSON) (map[string]string, error) {
	var root *CommandJSON
	var groupOrder []string
	groups := map[string][]CommandJSON{}
	for _, c := range cmds {
		fields := strings.Fields(c.Path)
		if len(fields) == 1 {
			c := c
			root = &c
			continue
		}
		g := fields[1]
		if g == "reference" {
			return nil, fmt.Errorf("command group %q collides with the cli-reference.md index page", g)
		}
		if _, ok := groups[g]; !ok {
			groupOrder = append(groupOrder, g)
		}
		groups[g] = append(groups[g], c)
	}
	sort.Strings(groupOrder)

	synopsis := func(g string) string {
		for _, c := range groups[g] {
			if c.Path == "sparkwing "+g {
				return c.Synopsis
			}
		}
		return ""
	}

	files := make(map[string]string, len(groups)+1)

	var idx strings.Builder
	idx.WriteString(generatedPageMarker)
	idx.WriteString("# CLI reference\n\n")
	idx.WriteString("Every `sparkwing` command, flag, and argument, generated from the " +
		"CLI's own command registry and split into one page per top-level " +
		"command group. For the conceptual overview -- which binaries exist, " +
		"the flag-naming rule, and what to reach for when -- see " +
		"[cli.md](cli.md).\n\n")
	idx.WriteString("## Command groups\n\n")
	for _, g := range groupOrder {
		line := "- [`sparkwing " + g + "`](cli-" + g + ".md)"
		if s := cell(synopsis(g)); s != "" {
			line += " -- " + s
		}
		idx.WriteString(line + "\n")
	}
	idx.WriteString("\n")
	if root != nil {
		writeCommandSection(&idx, *root, false)
	}
	files["cli-reference.md"] = strings.TrimRight(idx.String(), "\n") + "\n"

	for _, g := range groupOrder {
		var b strings.Builder
		b.WriteString(generatedPageMarker)
		b.WriteString("# CLI reference: sparkwing " + g + "\n\n")
		b.WriteString("Every `sparkwing " + g + "` command, flag, and argument, " +
			"generated from the CLI's own command registry. All command groups " +
			"are indexed in [cli-reference.md](cli-reference.md).\n\n")
		for _, c := range groups[g] {
			writeCommandSection(&b, c, true)
		}
		files["cli-"+g+".md"] = strings.TrimRight(b.String(), "\n") + "\n"
	}
	return files, nil
}

func writeSplitMarkdown(dir string, cmds []CommandJSON) error {
	files, err := splitCommandsMarkdown(cmds)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var wrote, unchanged, removed int
	for _, name := range names {
		path := filepath.Join(dir, name)
		if prev, rerr := os.ReadFile(path); rerr == nil && string(prev) == files[name] {
			unchanged++
			continue
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			return fmt.Errorf("commands: %w", err)
		}
		wrote++
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("commands: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "cli-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if _, ok := files[name]; ok {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil || !strings.HasPrefix(string(data), "<!-- GENERATED from the CLI command registry") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("commands: %w", err)
		}
		fmt.Printf("removed stale generated page %s\n", filepath.Join(dir, name))
		removed++
	}
	fmt.Printf("cli reference: wrote %d, unchanged %d, removed %d page(s) in %s\n", wrote, unchanged, removed, dir)
	return nil
}

func descBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		t := strings.TrimLeft(ln, " \t")
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") {
			indent := ln[:len(ln)-len(t)]
			lines[i] = indent + "\\" + t
		}
	}
	return strings.Join(lines, "\n")
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}
