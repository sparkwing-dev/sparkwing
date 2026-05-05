// `sparkwing configure xrepo` -- laptop-local repo registry CLI. Persists at
// ~/.config/sparkwing/repos.yaml; consumed by the local trigger
// consumer (pkg/orchestrator/local_trigger_loop.go) to resolve
// "pipeline X" -> "checkout at /Users/.../code/Y" without per-call
// WithAwaitRepo annotations.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/repos"
)

func runXrepo(args []string) error {
	if len(args) == 0 {
		printXrepoUsage(os.Stderr)
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
	case "-h", "--help":
		printXrepoUsage(os.Stdout)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "sparkwing configure xrepo: unknown subcommand %q\n\n", args[0])
		printXrepoUsage(os.Stderr)
		os.Exit(2)
	}
	return nil
}

func printXrepoUsage(w io.Writer) {
	fmt.Fprintln(w, "Manage the laptop-local repo registry")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "USAGE")
	fmt.Fprintln(w, "  sparkwing configure xrepo <subcommand>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "DESCRIPTION")
	fmt.Fprintln(w, "  The registry maps pipeline names to local checkouts so")
	fmt.Fprintln(w, "  cross-repo AwaitPipelineJob calls resolve without")
	fmt.Fprintln(w, "  hardcoded WithAwaitRepo annotations. Auto-populated when")
	fmt.Fprintln(w, "  you run `wing <pipeline>` in a .sparkwing/-bearing repo")
	fmt.Fprintln(w, "  (set SPARKWING_NO_AUTO_REGISTER=1 to disable).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "COMMANDS")
	fmt.Fprintln(w, "  list    Show every registered repo and the pipelines it provides")
	fmt.Fprintln(w, "  add     Register a checkout explicitly (path or .)")
	fmt.Fprintln(w, "  remove  Drop a registered repo by path or basename")
	fmt.Fprintln(w, "  prune   Drop registered repos whose .sparkwing/ no longer exists")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "OTHER")
	fmt.Fprintln(w, "  -h, --help  Show this help and exit")
}

// runXrepoList prints every registered repo and the pipelines it
// declares. Adds a stale/worktree marker so the operator can spot
// drift without reading the YAML by hand. --json emits a stable
// shape for agents.
func runXrepoList(args []string) error {
	fs := flag.NewFlagSet("repo list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON instead of the human table")
	pipelines := fs.Bool("pipelines", true,
		"include pipeline names (set --pipelines=false to skip the per-repo describe call)")
	_ = fs.Parse(args)

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
			// pipelineNamesForRepo lives in pkg/repos but isn't
			// exported -- the resolver builds its own map. Replay
			// describe here via the same caching path: the
			// resolver's process-wide map is rebuilt lazily so a
			// fresh `repo list` after a registry edit shows the
			// current pipelines.
			repos.InvalidateCache()
			if pipes, perr := repoListPipelines(e.Path); perr == nil {
				sort.Strings(pipes)
				row.Pipelines = pipes
			}
		}
		rows = append(rows, row)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	if len(rows) == 0 {
		fmt.Println("no repos registered")
		fmt.Println("(register with `sparkwing configure xrepo add <path>` or just run `wing` in a .sparkwing/-bearing repo)")
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
	_ = fs.Parse(args)
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
	_ = fs.Parse(args)
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
	_ = fs.Parse(args)
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
	// Use the resolver's defaultResolver build path indirectly by
	// resolving a sentinel that won't match -- this populates the
	// nameToPath map and we read out names whose path equals our
	// row. ResolveRepoForPipeline returns ErrNotFound for the
	// sentinel; we don't care.
	_, _ = repos.ResolveRepoForPipeline("__sparkwing_repo_list_probe__")
	// We don't have a public accessor to the populated map, so
	// fall back to a fresh describe via the same code path the
	// resolver uses internally.
	return repos.PipelineNamesForRepo(absPath)
}
