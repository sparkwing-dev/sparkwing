package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

type repoInfo struct {
	Name         string         `json:"name"`
	Primary      string         `json:"primary,omitempty"`
	Status       string         `json:"status"`
	Pin          string         `json:"pin,omitempty"`
	Replace      string         `json:"replace,omitempty"`
	Latest       string         `json:"latest,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	Commit       string         `json:"commit,omitempty"`
	Dirty        bool           `json:"dirty"`
	GuidesBehind int            `json:"guides_behind"`
	Guides       []repoGuide    `json:"guides,omitempty"`
	Worktrees    []repoWorktree `json:"worktrees,omitempty"`
	Pipelines    []repoPipeline `json:"pipelines,omitempty"`
	Suggestion   string         `json:"suggestion,omitempty"`
}

type repoGuide struct {
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type repoWorktree struct {
	Path     string `json:"path"`
	Pin      string `json:"pin,omitempty"`
	Diverges bool   `json:"diverges"`
}

type repoPipeline struct {
	Name       string `json:"name"`
	LastRun    string `json:"last_run,omitempty"`
	LastStatus string `json:"last_status,omitempty"`

	lastNumber int64
}

func runReposInfo(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	output := inv.Output(color.IsInteractiveStdout())
	repoSel, err := invocationCheckout(inv)
	if err != nil {
		return err
	}

	latest, _ := fetchLatestRelease()
	fleet, err := buildFleet(latest)
	if err != nil {
		return err
	}
	repo, err := selectRepo(fleet, repoSel)
	if err != nil {
		return err
	}

	ctx := context.Background()
	reader, closeTransport, err := resolveRunReader(ctx, "")
	if err != nil {
		return err
	}
	defer closeTransport()
	info, err := buildRepoInfo(ctx, repo, latest, reader)
	if err != nil {
		return err
	}
	if strings.ToLower(output) == "json" {
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(info)
	}
	if output == "plain" {
		_, err := fmt.Fprintln(os.Stdout, repo.Primary)
		return err
	}
	printRepoInfo(info)
	return nil
}

func selectRepo(fleet []repos.Repo, sel string) (repos.Repo, error) {
	if sel != "" {
		scoped := scopeFleet(fleet, sel)
		if len(scoped) == 0 {
			return repos.Repo{}, fmt.Errorf("repos info: no tracked repo matches %q", sel)
		}
		return scoped[0], nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return repos.Repo{}, err
	}
	root, err := repos.PrimaryRoot(fleetGit, cwd)
	if err == nil && root != "" {
		for _, r := range fleet {
			if r.Primary == root {
				return r, nil
			}
		}
	}
	for _, r := range fleet {
		if r.Primary != "" {
			relative, err := filepath.Rel(r.Primary, cwd)
			if err == nil && filepath.IsLocal(relative) {
				return r, nil
			}
		}
	}
	return repos.Repo{}, fmt.Errorf("repos info: %q is not a registered sparkwing repo; select a registered checkout with -C PATH", cwd)
}

func buildRepoInfo(ctx context.Context, repo repos.Repo, latest string, reader controllerwire.RunReader) (repoInfo, error) {
	info := repoInfo{
		Name:         repo.Name,
		Primary:      repo.Primary,
		Status:       repo.Status,
		Pin:          repo.Pin,
		Replace:      repo.Replace,
		Latest:       latest,
		GuidesBehind: repo.GuidesBehind,
	}
	if entries, err := docs.MigrationsBetween(repo.Pin, latest); err == nil {
		for _, e := range entries {
			info.Guides = append(info.Guides, repoGuide{Version: e.Version, Title: e.Title, Summary: e.Summary})
		}
	}
	for _, w := range repo.Worktrees {
		info.Worktrees = append(info.Worktrees, repoWorktree{
			Path:     w.Path,
			Pin:      w.Pin,
			Diverges: w.Pin != "" && w.Pin != repo.Pin,
		})
	}
	if repo.Primary != "" {
		if dirty, err := (execOps{}).Dirty(repo.Primary); err == nil {
			info.Dirty = dirty
		}
		if out, err := runGit(repo.Primary, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			info.Branch = strings.TrimSpace(out)
		}
		if out, err := runGit(repo.Primary, "rev-parse", "--short", "HEAD"); err == nil {
			info.Commit = strings.TrimSpace(out)
		}
	}
	var err error
	info.Pipelines, err = pipelineStates(ctx, repo, reader)
	if err != nil {
		return repoInfo{}, err
	}
	info.Suggestion = repoSuggestion(info)
	return info, nil
}

func pipelineStates(ctx context.Context, repo repos.Repo, reader controllerwire.RunReader) ([]repoPipeline, error) {
	byName := map[string]*repoPipeline{}
	var order []string
	add := func(name string) *repoPipeline {
		p, ok := byName[name]
		if !ok {
			p = &repoPipeline{Name: name}
			byName[name] = p
			order = append(order, name)
		}
		return p
	}
	if repo.Primary != "" {
		if names, ok := repos.PipelineNamesIfBuilt(repo.Primary); ok {
			for _, n := range names {
				add(n)
			}
		}
	}
	err := visitRunPages(ctx, reader, controllerwire.RunQuery{Limit: 500}, func(page controllerwire.RunPage) error {
		for _, run := range page.Runs {
			if !repos.RunMatchesRepo(repos.RunObservation{Repo: run.Inputs.Git.Repository.Display}, repo) {
				continue
			}
			p := add(run.Inputs.Pipeline)
			if run.Number > p.lastNumber {
				p.lastNumber, p.LastStatus = run.Number, run.Status
				p.LastRun = ""
				if run.StartedAt != nil {
					p.LastRun = run.StartedAt.Format("2006-01-02 15:04")
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]repoPipeline, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func repoSuggestion(info repoInfo) string {
	if info.GuidesBehind > 0 && info.Replace == "" && info.Latest != "" {
		return fmt.Sprintf("pin is %d guide(s) behind: sparkwing repos update --version %s --in-place", info.GuidesBehind, info.Latest)
	}
	if info.Dirty {
		return "working tree has uncommitted changes; commit or stash before bumping the SDK pin"
	}
	return ""
}

func printRepoInfo(info repoInfo) {
	fmt.Printf("%s", info.Name)
	if info.Status != "" && info.Status != "ok" {
		fmt.Printf("  (%s)", info.Status)
	}
	fmt.Println()
	if info.Primary != "" {
		fmt.Printf("  %s\n", color.Dim(info.Primary))
	}

	pin := orDashStr(info.Pin)
	if info.Replace != "" {
		pin = "replaced -> " + info.Replace
	}
	fmt.Printf("  SDK pin:   %s", pin)
	if info.Latest != "" {
		fmt.Printf("   (latest %s)", info.Latest)
	}
	if info.GuidesBehind > 0 {
		fmt.Printf("   %s", color.Yellow(fmt.Sprintf("%d guide(s) behind", info.GuidesBehind)))
	}
	fmt.Println()

	tree := "clean"
	if info.Dirty {
		tree = color.Yellow("uncommitted changes")
	}
	if info.Branch != "" || info.Commit != "" {
		fmt.Printf("  worktree:  %s on %s %s\n", tree, orDashStr(info.Branch), color.Dim(info.Commit))
	} else if info.Primary != "" {
		fmt.Printf("  worktree:  %s\n", tree)
	}

	if len(info.Guides) > 0 {
		fmt.Printf("  migration guides in range:\n")
		for _, g := range info.Guides {
			title := g.Title
			if title == "" {
				title = g.Summary
			}
			fmt.Printf("      %-10s %s\n", g.Version, color.Dim(title))
			if g.Summary != "" && g.Title != "" {
				fmt.Printf("                 %s\n", color.Dim(g.Summary))
			}
		}
	}

	if len(info.Worktrees) > 0 {
		fmt.Printf("  worktrees:\n")
		for _, w := range info.Worktrees {
			flagTxt := ""
			if w.Diverges {
				flagTxt = color.Yellow("  (diverges)")
			}
			fmt.Printf("      %-14s %s%s\n", orDashStr(w.Pin), color.Dim(w.Path), flagTxt)
		}
	}

	if len(info.Pipelines) > 0 {
		fmt.Printf("  pipelines:\n")
		for _, p := range info.Pipelines {
			last := "never run"
			if p.LastRun != "" {
				last = p.LastRun + " " + p.LastStatus
			}
			fmt.Printf("      %-24s %s\n", p.Name, color.Dim(last))
		}
	}

	if info.Suggestion != "" {
		fmt.Printf("\n%s %s\n", color.Yellow("next:"), info.Suggestion)
	}
}
