package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// repoInfo is the read-only deep dive of a single fleet member: its SDK pin
// and how far behind it is, its worktrees, working-tree state, whether its pin
// can open the machine's state database, and its pipelines with last-run
// status. It is the JSON shape and the source for the pretty view.
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
	Schema       repoSchema     `json:"schema"`
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

type repoSchema struct {
	DBVersion  int    `json:"db_version"`
	MinVersion string `json:"min_version,omitempty"`
	PinOpensDB bool   `json:"pin_opens_db"`
	Note       string `json:"note,omitempty"`
}

type repoPipeline struct {
	Name       string `json:"name"`
	LastRun    string `json:"last_run,omitempty"`
	LastStatus string `json:"last_status,omitempty"`

	lastAt time.Time
}

func runReposInfo(args []string) error {
	fs := flag.NewFlagSet(cmdReposInfo.Path, flag.ContinueOnError)
	var repoSel, output string
	fs.StringVar(&repoSel, "repo", "", "repo name or checkout path; default: the repo containing the current directory")
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	if err := parseAndCheck(cmdReposInfo, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("repos info: unexpected positional %q", fs.Arg(0))
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

	info := buildRepoInfo(context.Background(), repo, latest)
	if strings.ToLower(output) == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	printRepoInfo(info)
	return nil
}

// selectRepo resolves which fleet member to describe: the one named by --repo,
// or -- when no selector is given -- the repo whose primary checkout contains
// the current directory.
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
		if r.Primary != "" && strings.HasPrefix(cwd, r.Primary) {
			return r, nil
		}
	}
	return repos.Repo{}, fmt.Errorf("repos info: %q is not a registered sparkwing repo; pass --repo NAME", cwd)
}

func buildRepoInfo(ctx context.Context, repo repos.Repo, latest string) repoInfo {
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
	info.Schema = schemaCompat(ctx, repo)
	info.Pipelines = pipelineStates(ctx, repo)
	info.Suggestion = repoSuggestion(info)
	return info
}

// schemaCompat reports whether this repo's pin can open the machine's state
// database. The database records the minimum binary version it will admit; a
// repo pinned below that has pipelines that would be refused before a run
// starts. A replaced SDK or a database with no stamp cannot be judged and is
// reported as such rather than guessed.
func schemaCompat(ctx context.Context, repo repos.Repo) repoSchema {
	sc := repoSchema{PinOpensDB: true}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		sc.Note = "no state database path"
		return sc
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		sc.Note = "state database could not be opened by this binary"
		return sc
	}
	defer func() { _ = st.Close() }()
	if v, err := st.CurrentSchemaVersion(ctx); err == nil {
		sc.DBVersion = v
	}
	sc.MinVersion = st.MinBinaryVersion(ctx)
	sc.PinOpensDB, sc.Note = schemaVerdict(repo.Pin, repo.Replace, sc.MinVersion)
	return sc
}

// schemaVerdict decides whether a repo's SDK pin can open a state database that
// requires minVersion, and explains the call. A replaced SDK, an unresolved
// pin, an unstamped database, or an uncomparable version cannot be judged
// against, so they default to "opens it" with a note rather than a false alarm.
func schemaVerdict(pin, replace, minVersion string) (opensDB bool, note string) {
	switch {
	case replace != "":
		return true, "SDK replaced with a local module; schema compatibility depends on that checkout"
	case pin == "":
		return true, "no SDK pin resolved for this repo"
	case minVersion == "":
		return true, "database has no minimum-version stamp; any pin may open it"
	case !semver.IsValid(pin) || !semver.IsValid(minVersion):
		return true, "pin or database minimum is not a comparable version"
	case semver.Compare(pin, minVersion) < 0:
		return false, fmt.Sprintf("pin %s is below the database minimum %s; a run would be refused", pin, minVersion)
	default:
		return true, fmt.Sprintf("pin %s satisfies the database minimum %s", pin, minVersion)
	}
}

// pipelineStates lists the repo's pipelines with their most recent run time
// and status. Declared names come from the already-built pipeline binary when
// one is cached (never triggering a build); run history fills in last-run and
// surfaces any pipeline that ran but is no longer declared.
func pipelineStates(ctx context.Context, repo repos.Repo) []repoPipeline {
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
	if paths, err := orchestrator.DefaultPaths(); err == nil {
		if st, err := store.Open(paths.StateDB()); err == nil {
			defer func() { _ = st.Close() }()
			if runs, err := st.ListRuns(ctx, store.RunFilter{Limit: 1000}); err == nil {
				for _, r := range runs {
					if !repos.RunMatchesRepo(repos.RunObservation{Repo: r.Repo, RepoURL: r.RepoURL}, repo) {
						continue
					}
					p := add(r.Pipeline)
					if p.lastAt.IsZero() || r.StartedAt.After(p.lastAt) {
						p.lastAt = r.StartedAt
						p.LastRun = r.StartedAt.Format("2006-01-02 15:04")
						p.LastStatus = r.Status
					}
				}
			}
		}
	}
	out := make([]repoPipeline, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// repoSuggestion surfaces the single most pressing next step when the repo is
// off, or "" when nothing needs doing. A pin that cannot open the shared
// database is the most urgent because a run fails outright; a stale pin and an
// uncommitted tree follow.
func repoSuggestion(info repoInfo) string {
	if !info.Schema.PinOpensDB && info.Schema.MinVersion != "" && info.Replace == "" {
		return fmt.Sprintf("pin cannot open the machine state DB (needs >= %s): sparkwing repos update --version %s --apply",
			info.Schema.MinVersion, info.Schema.MinVersion)
	}
	if info.GuidesBehind > 0 && info.Replace == "" && info.Latest != "" {
		return fmt.Sprintf("pin is %d guide(s) behind: sparkwing repos update --version %s --apply", info.GuidesBehind, info.Latest)
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

	fmt.Printf("  state DB:  ")
	if info.Schema.PinOpensDB {
		fmt.Printf("%s", color.Green("pin opens it"))
	} else {
		fmt.Printf("%s", color.Red("pin cannot open it"))
	}
	if info.Schema.DBVersion > 0 {
		fmt.Printf("   (schema %d", info.Schema.DBVersion)
		if info.Schema.MinVersion != "" {
			fmt.Printf(", needs >= %s", info.Schema.MinVersion)
		}
		fmt.Printf(")")
	}
	fmt.Println()
	if info.Schema.Note != "" {
		fmt.Printf("      %s\n", color.Dim(info.Schema.Note))
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
