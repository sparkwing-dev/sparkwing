// `sparkwing commands` exposes the entire CLI surface as structured
// data so an agent learns every verb in one tool call. Each entry
// is the same Command record the help renderer uses; we just emit
// it as JSON instead of prose.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/color"
)

// allCommands lists every Command registered in help_registry.go.
// Adding a new Command means adding it here too -- the
// TestAllCommandsAreRegistered guard test fails CI if anyone forgets.
var allCommands = []*Command{
	&cmdSparkwing, &cmdInfo, &cmdCluster, &cmdCommands, &cmdUpdate, &cmdVersion, &cmdVersionUpdate, &cmdRun,
	&cmdConfigure, &cmdConfigureInit,
	&cmdDocs, &cmdDocsList, &cmdDocsRead, &cmdDocsAll, &cmdDocsSearch,
	&cmdDebug, &cmdDebugRun, &cmdDebugRelease, &cmdDebugAttach,
	&cmdDebugRerun, &cmdDebugReplay, &cmdDebugEnv,
	&cmdWing,
	&cmdPipeline, &cmdPipelineList, &cmdPipelineDescribe, &cmdPipelineDiscover,
	&cmdPipelineNew, &cmdPipelineExplain, &cmdPipelinePlan, &cmdPipelineRun,
	&cmdDashboard, &cmdDashboardStart, &cmdDashboardKill, &cmdDashboardStatus,
	&cmdWorker, &cmdGC, &cmdCompletion,
	&cmdProfiles, &cmdProfilesAdd, &cmdProfilesList, &cmdProfilesShow,
	&cmdProfilesUse, &cmdProfilesRemove, &cmdProfilesDuplicate,
	&cmdProfilesSet, &cmdProfilesTest,
	&cmdTokens, &cmdTokensCreate, &cmdTokensList, &cmdTokensRevoke,
	&cmdTokensLookup, &cmdTokensRotate,
	&cmdUsers, &cmdUsersAdd, &cmdUsersList, &cmdUsersDelete,
	&cmdJobs, &cmdJobsList, &cmdJobsStatus, &cmdJobsLogs, &cmdJobsErrors,
	&cmdJobsFailures, &cmdJobsStats, &cmdJobsLast, &cmdJobsTree,
	&cmdJobsGet, &cmdJobsReceipt, &cmdJobsWait, &cmdJobsFind, &cmdJobsRetry,
	&cmdJobsCancel, &cmdJobsPrune,
	&cmdPush,
	&cmdHooks, &cmdHooksInstall, &cmdHooksUninstall, &cmdHooksStatus,
	&cmdSecret, &cmdSecretSet, &cmdSecretGet, &cmdSecretList, &cmdSecretDelete,
	&cmdTriggers, &cmdTriggersList, &cmdTriggersGet,
	&cmdImage, &cmdImageRollout,
	&cmdHealth,
	&cmdWebhooks, &cmdWebhooksList, &cmdWebhooksDeliveries, &cmdWebhooksReplay,
	&cmdAgents, &cmdAgentsList,
	&cmdSparks, &cmdSparksList, &cmdSparksLint, &cmdSparksResolve,
	&cmdSparksUpdate, &cmdSparksAdd, &cmdSparksRemove, &cmdSparksWarmup,
	&cmdApprove, &cmdDeny, &cmdApprovals, &cmdApprovalsList,
}

// CommandJSON is the wire shape emitted by `sparkwing commands` and
// `<any-verb> --help --json`. It mirrors the Command struct but
// flattens the FlagSpec / SubcommandRef / Example / PosArg types to
// public-friendly field names. Decoupled from internal Command so
// renames don't silently break agents pinning to older fields.
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
	for _, s := range c.Subcommands {
		out.Subcommands = append(out.Subcommands, SubcommandJSON{Name: s.Name, Synopsis: s.Synopsis})
	}
	for _, p := range c.PosArgs {
		out.PosArgs = append(out.PosArgs, PosArgJSON{Name: p.Name, Desc: p.Desc, Required: p.Required})
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

// runCommands handles `sparkwing commands [--include-hidden]
// [--path PREFIX] [-o json|plain]`. Default --output json because
// agents are the primary audience; -o plain emits one path per line.
func runCommands(args []string) error {
	fs := flag.NewFlagSet(cmdCommands.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "json", "json | plain")
	includeHidden := fs.Bool("include-hidden", false, "also emit Hidden:true commands (default: skip)")
	pathPrefix := fs.String("path", "", "only emit commands whose Path starts with this prefix")
	if err := parseAndCheck(cmdCommands, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("commands: unexpected positional %q", fs.Arg(0))
	}

	// Sort by Path so the output is reproducible and agents diffing
	// successive emissions see meaningful changes.
	sorted := make([]*Command, len(allCommands))
	copy(sorted, allCommands)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var picked []CommandJSON
	for _, c := range sorted {
		if c.Hidden && !*includeHidden {
			continue
		}
		if *pathPrefix != "" && !strings.HasPrefix(c.Path, *pathPrefix) {
			continue
		}
		picked = append(picked, toCommandJSON(c))
	}

	switch strings.ToLower(output) {
	case "json", "":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(picked)
	case "plain":
		for _, c := range picked {
			fmt.Println(c.Path)
		}
		return nil
	case "table":
		// Token effort -- a wide table of every verb is rarely the
		// right view for humans; they should use sparkwing -h. Render
		// a thin two-column table so the verb is at least usable.
		w := 0
		for _, c := range picked {
			if n := len(c.Path); n > w {
				w = n
			}
		}
		// Pad before coloring so ANSI bytes in the header don't
		// throw off %-*s width tracking.
		fmt.Printf("%s  %s\n",
			color.Bold(fmt.Sprintf("%-*s", w, "PATH")),
			color.Bold("SYNOPSIS"))
		for _, c := range picked {
			fmt.Printf("%-*s  %s\n", w, c.Path, color.Dim(c.Synopsis))
		}
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: json, plain, table)", output)
	}
}
