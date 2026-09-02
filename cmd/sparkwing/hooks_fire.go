package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func runHooksFire(args []string) error {
	fs := flag.NewFlagSet(cmdHooksFire.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	fleet := fs.Bool("fleet", false, "fire the gate in every registered repo")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdHooksFire, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *fleet && *repo != "" {
		return errors.New("hooks fire: --fleet fires in every registered repo; drop --repo or drop --fleet")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdHooksFire.Path)
	if err != nil {
		return err
	}

	var results []githooks.FireResult
	if *fleet {
		roots, err := fleetRepoRoots(runGit)
		if err != nil {
			return fmt.Errorf("hooks fire: %w", err)
		}
		for _, root := range roots {
			declared, err := declaredHookNames(root)
			if err != nil {
				return fmt.Errorf("hooks fire: %w", err)
			}
			results = append(results, githooks.Fire(runGit, root, declared))
		}
	} else {
		repoRoot, _, err := resolveHooksRepo(*repo)
		if err != nil {
			return fmt.Errorf("hooks fire: %w", err)
		}
		declared, err := declaredHookNames(repoRoot)
		if err != nil {
			return fmt.Errorf("hooks fire: %w", err)
		}
		results = append(results, githooks.Fire(runGit, repoRoot, declared))
	}
	if err := renderHooksFire(os.Stdout, results, format); err != nil {
		return err
	}
	if unenforced := unenforcedResults(results); len(unenforced) > 0 {
		return fmt.Errorf("hooks fire: %d repo(s) did not refuse a commit", len(unenforced))
	}
	return nil
}

func unenforcedResults(results []githooks.FireResult) []githooks.FireResult {
	var out []githooks.FireResult
	for _, r := range results {
		if r.Verdict == githooks.FireUndeclared || r.Enforced() {
			continue
		}
		out = append(out, r)
	}
	return out
}

func renderHooksFire(w io.Writer, results []githooks.FireResult, format string) error {
	switch format {
	case "json":

		return ndjson.Write(w, results)
	case "plain":
		for _, r := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Repo, r.Verdict, r.Hook)
		}
		return nil
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "no repos registered; run `sparkwing pipeline add <dir>` first")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tVERDICT\tREFUSED BY")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", filepath.Base(r.Repo), r.Verdict, hookOrDash(r.Hook))
	}
	_ = tw.Flush()

	unenforced := unenforcedResults(results)
	if len(unenforced) == 0 {
		fmt.Fprintf(w, "\n%d repo(s): every gate refused the commit it was given\n", len(results))
		return nil
	}
	fmt.Fprintf(w, "\n%d of %d repo(s) did not refuse a commit with a gate of their own:\n", len(unenforced), len(results))
	for _, r := range unenforced {
		fmt.Fprintf(w, "  %s\n", r.Summary())
		if r.HeadMoved {
			fmt.Fprintf(w, "    HEAD moved during the attempt, which no verdict here should cause; inspect %s before trusting anything else in this report\n", r.Repo)
		}
		if remedy := fireRemedy(r); remedy != "" {
			fmt.Fprintf(w, "    %s\n", remedy)
		}
	}
	return nil
}

func fireRemedy(r githooks.FireResult) string {
	switch r.Verdict {
	case githooks.FireBorrowed:
		return fmt.Sprintf("git -C %s config --unset core.hooksPath, then sparkwing pipeline hooks install --repo %s", r.Repo, r.Repo)
	case githooks.FireAccepted, githooks.FireUnprovable:
		return fmt.Sprintf("sparkwing pipeline hooks install --repo %s", r.Repo)
	default:
		return ""
	}
}

func hookOrDash(hook string) string {
	if strings.TrimSpace(hook) == "" {
		return "-"
	}
	return hook
}
