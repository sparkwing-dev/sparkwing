// `sparkwing configure xrepo` -- laptop-local repo registry CLI. Persists at
// ~/.config/sparkwing/repos.yaml; consumed by the local trigger
// consumer (internal/orchestrator/local_trigger_loop.go) to resolve
// "pipeline X" -> "checkout at ~/code/Y" without per-call
// WithFreshRepo annotations.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

func runXrepo(args []string) error {
	if handleParentHelp(cmdConfigureXrepo, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdConfigureXrepo, os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "list", "ls":
		return runXrepoList(args[1:])
	case "add":
		return runXrepoAdd(args[1:])
	case "remove", "rm":
		return runXrepoRemove(args[1:])
	case "prune":
		return runXrepoPrune(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "sparkwing configure xrepo: unknown subcommand %q\n\n", args[0])
		PrintHelp(cmdConfigureXrepo, os.Stderr)
		os.Exit(2)
	}
	return nil
}

// runXrepoList prints every registered repo and the pipelines it
// declares. Adds a stale/worktree marker so the operator can spot
// drift without reading the YAML by hand. `-o json` emits a stable
// shape for agents.
func runXrepoList(args []string) error {
	fs := flag.NewFlagSet("repo list", flag.ExitOnError)
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	pipelines := fs.Bool("pipelines", true,
		"include pipeline names (set --pipelines=false to skip the per-repo describe call)")
	if err := parseAndCheck(cmdConfigureXrepoList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	entries, err := repos.List()
	if err != nil {
		return err
	}
	type rowOut struct {
		Path      string   `json:"path"`
		Status    string   `json:"status"`
		Worktree  bool     `json:"worktree,omitempty"`
		Pipelines []string `json:"pipelines,omitempty"`
	}
	rows := make([]rowOut, 0, len(entries))
	for _, e := range entries {
		row := rowOut{Path: e.Path, Status: e.Status, Worktree: e.Worktree}
		if *pipelines && e.Status == "ok" {
			repos.InvalidateCache()
			if pipes, perr := repoListPipelines(e.Path); perr == nil {
				sort.Strings(pipes)
				row.Pipelines = pipes
			}
		}
		rows = append(rows, row)
	}

	if *outputFormat == "json" {
		// NDJSON: one repo per line.
		return ndjson.Write(os.Stdout, rows)
	}

	if len(rows) == 0 {
		fmt.Println("no repos registered")
		fmt.Println("(register with `sparkwing configure xrepo add <path>` or just run `sparkwing run` in a .sparkwing/-bearing repo)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tPATH\tPIPELINES")
	for _, r := range rows {
		status := r.Status
		if r.Worktree {
			status += " (worktree)"
		}
		pipelinesCol := strings.Join(r.Pipelines, ", ")
		if !*pipelines {
			pipelinesCol = "-"
		}
		if pipelinesCol == "" {
			pipelinesCol = "(describe failed)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", status, r.Path, pipelinesCol)
	}
	return tw.Flush()
}

// runXrepoAdd registers an explicit path (default: cwd) in the
// registry. Unlike auto-register this does NOT skip worktrees --
// the user asked for it.
func runXrepoAdd(args []string) error {
	fs := flag.NewFlagSet("repo add", flag.ExitOnError)
	if err := parseAndCheck(cmdConfigureXrepoAdd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	target := "."
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("absolute %s: %w", target, err)
	}
	if err := repos.Add(abs); err != nil {
		return err
	}
	fmt.Printf("registered: %s\n", abs)
	return nil
}

// runXrepoRemove drops every entry matching the argument: full
// path, abbreviated path, or basename ("sparkwing" matches a
// registered ~/code/sparkwing). Zero matches isn't an error -- the
// user's intent is "I don't want this" and that's already true.
func runXrepoRemove(args []string) error {
	fs := flag.NewFlagSet("repo remove", flag.ExitOnError)
	if err := parseAndCheck(cmdConfigureXrepoRemove, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: sparkwing configure xrepo remove <path-or-basename>")
	}
	match := fs.Arg(0)
	n, err := repos.Remove(match)
	if err != nil {
		return err
	}
	fmt.Printf("removed %d %s matching %q\n", n, entryWord(n), match)
	return nil
}

// runXrepoPrune drops entries whose path no longer has a .sparkwing/
// dir. Useful after moving / deleting a checkout.
func runXrepoPrune(args []string) error {
	fs := flag.NewFlagSet("repo prune", flag.ExitOnError)
	if err := parseAndCheck(cmdConfigureXrepoPrune, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	dropped, err := repos.Prune()
	if err != nil {
		return err
	}
	if len(dropped) == 0 {
		fmt.Println("registry is clean (no stale entries)")
		return nil
	}
	fmt.Printf("pruned %d stale %s:\n", len(dropped), entryWord(len(dropped)))
	for _, p := range dropped {
		fmt.Printf("  %s\n", p)
	}
	return nil
}

// entryWord pluralizes "entry"/"entries" for repo CLI messages.
// Tiny helper kept local because cmd/sparkwing's existing plural()
// only handles -es words.
func entryWord(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}

// repoListPipelines mirrors pkg/repos.pipelineNamesForRepo. We
// re-do the work here (rather than exporting the helper) because
// the CLI is the only consumer that wants pipeline names alongside
// a list -- the orchestrator's resolver uses an in-memory map. If
// a third caller materializes, hoist this into pkg/repos.
func repoListPipelines(absPath string) ([]string, error) {
	_, _ = repos.ResolveRepoForPipeline("__sparkwing_repo_list_probe__")
	return repos.PipelineNamesForRepo(absPath)
}
