package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

// cronsInstallReport is what one `crons install` did, across one repo or the
// whole fleet.
type cronsInstallReport struct {
	Repos       []cronsRepoResult `json:"repos"`
	Armed       int               `json:"armed"`
	Refreshed   int               `json:"refreshed"`
	Withdrawn   int               `json:"withdrawn"`
	NothingToDo int               `json:"nothing_to_arm"`
	Timer       *crontimer.State  `json:"timer,omitempty"`
	TimerSkip   string            `json:"timer_skipped,omitempty"`
}

type cronsRepoResult struct {
	Repo        string   `json:"repo"`
	Schedules   []string `json:"schedules,omitempty"`
	Withdrawals []string `json:"withdrawals,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func runCronsInstall(args []string) error {
	fs := flag.NewFlagSet(cmdCronsInstall.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	fleet := fs.Bool("fleet", false, "arm every registered repo")
	noProve := fs.Bool("no-prove", false, "arm without compiling the pipelines first")
	noTimer := fs.Bool("no-timer", false, "arm without installing the OS timer")
	if err := fs.MarkHidden("no-timer"); err != nil {
		return err
	}
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsInstall, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *fleet && *repo != "" {
		return errors.New("crons install: --fleet arms every registered repo; drop --repo or drop --fleet")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsInstall.Path)
	if err != nil {
		return err
	}

	roots, err := cronsTargetRoots(*repo, *fleet)
	if err != nil {
		return fmt.Errorf("crons install: %w", err)
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons install: %w", err)
	}
	defer release()

	ctx := context.Background()
	prove := cronsProver(*noProve)
	report := cronsInstallReport{}
	var failed []string
	for _, root := range roots {
		result := cronsRepoResult{Repo: root}
		armed, aerr := session.svc.Arm(ctx, root, prove)
		switch {
		case aerr != nil:
			result.Error = aerr.Error()
			failed = append(failed, filepath.Base(root))
		case len(armed.Schedules) == 0 && armed.Withdrawn == 0:
			report.NothingToDo++
		default:
			for _, s := range armed.Schedules {
				result.Schedules = append(result.Schedules, crons.DisplayName(s))
			}
			result.Withdrawals = armed.Withdrawals
			report.Armed += armed.Armed
			report.Refreshed += armed.Refreshed
			report.Withdrawn += armed.Withdrawn
		}
		report.Repos = append(report.Repos, result)
	}

	if !*noTimer {
		if terr := ensureCronsTimer(ctx, session, &report); terr != nil {
			return fmt.Errorf("crons install: %w", terr)
		}
	} else {
		report.TimerSkip = "--no-timer: " + unsupportedTimerHint
	}

	if err := renderCronsInstall(report, format); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("crons install: %s", strings.Join(failed, ", "))
	}
	return nil
}

// ensureCronsTimer writes the OS timer when this host has something armed and
// no current timer of its own. A host with nothing armed keeps no timer.
func ensureCronsTimer(ctx context.Context, session *cronsSession, report *cronsInstallReport) error {
	rows, err := session.svc.List(ctx)
	if err != nil {
		return err
	}
	armed := 0
	for _, r := range rows {
		if r.State != crons.StateUndeclared {
			armed++
		}
	}
	if armed == 0 {
		report.TimerSkip = "nothing is armed here, so no timer was installed"
		return nil
	}
	host, err := cronsTimerHost(session.paths)
	if err != nil {
		return err
	}
	state, err := crontimer.Status(host)
	if errors.Is(err, crontimer.ErrUnsupported) {
		return fmt.Errorf("%w.\nThe schedules are armed; %s", err, unsupportedTimerHint)
	}
	if err != nil {
		return err
	}
	if state.Installed && state.Enabled && !state.Stale {
		report.Timer = &state
		return nil
	}
	installed, err := crontimer.Install(host)
	if err != nil {
		return err
	}
	report.Timer = &installed
	return nil
}

func renderCronsInstall(report cronsInstallReport, format string) error {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	multi := len(report.Repos) > 1
	for _, r := range report.Repos {
		if multi {
			fmt.Fprintf(os.Stdout, "\n=== %s\n", r.Repo)
		}
		switch {
		case r.Error != "":
			fmt.Fprintf(os.Stdout, "error: %s\n", r.Error)
		case len(r.Schedules) == 0 && len(r.Withdrawals) == 0:
			if !multi {
				fmt.Fprintf(os.Stdout, "nothing to arm: no pipeline in %s declares a schedule\n", r.Repo)
			}
		default:
			for _, name := range r.Schedules {
				fmt.Fprintf(os.Stdout, "armed %s\n", name)
			}
			for _, name := range r.Withdrawals {
				fmt.Fprintf(os.Stdout, "withdrawn %s: the repo no longer declares it\n", name)
			}
		}
	}
	if multi {
		fmt.Fprintf(os.Stdout, "\n%d schedule(s) armed, %d refreshed, %d withdrawn, %d repo(s) with nothing to arm\n",
			report.Armed, report.Refreshed, report.Withdrawn, report.NothingToDo)
	}
	switch {
	case report.Timer != nil:
		fmt.Fprintf(os.Stdout, "timer: %s\n", report.Timer.Detail)
	case report.TimerSkip != "":
		fmt.Fprintf(os.Stdout, "timer: %s\n", report.TimerSkip)
	}
	return nil
}

func runCronsUninstall(args []string) error {
	fs := flag.NewFlagSet(cmdCronsUninstall.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	fleet := fs.Bool("fleet", false, "disarm every registered repo")
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsUninstall, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *fleet && *repo != "" {
		return errors.New("crons uninstall: --fleet disarms every registered repo; drop --repo or drop --fleet")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsUninstall.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons uninstall: %w", err)
	}
	defer release()

	ctx := context.Background()
	var roots []string
	if *fleet {
		rows, lerr := session.svc.List(ctx)
		if lerr != nil {
			return fmt.Errorf("crons uninstall: %w", lerr)
		}
		seen := map[string]bool{}
		for _, r := range rows {
			if !seen[r.RepoPath] {
				seen[r.RepoPath] = true
				roots = append(roots, r.RepoPath)
			}
		}
	} else {
		roots, err = cronsTargetRoots(*repo, false)
		if err != nil {
			return fmt.Errorf("crons uninstall: %w", err)
		}
	}

	report := cronsUninstallReport{}
	for _, root := range roots {
		removed, derr := session.svc.Disarm(ctx, root)
		if derr != nil {
			return fmt.Errorf("crons uninstall: %w", derr)
		}
		report.Repos = append(report.Repos, cronsDisarmed{Repo: root, Removed: removed})
		report.Removed += removed
	}

	remaining, err := session.svc.List(ctx)
	if err != nil {
		return fmt.Errorf("crons uninstall: %w", err)
	}
	report.Remaining = len(remaining)
	if report.Remaining == 0 {
		host, herr := cronsTimerHost(session.paths)
		if herr != nil {
			return fmt.Errorf("crons uninstall: %w", herr)
		}
		state, terr := crontimer.Uninstall(host)
		switch {
		case errors.Is(terr, crontimer.ErrUnsupported):
			report.TimerDetail = "no OS timer runs the tick on this platform"
		case terr != nil:
			return fmt.Errorf("crons uninstall: %w", terr)
		default:
			report.Timer = &state
			report.TimerDetail = state.Detail
		}
	} else {
		report.TimerDetail = fmt.Sprintf("%d schedule(s) are still armed, so the timer stays", report.Remaining)
	}
	return renderCronsUninstall(report, format)
}

type cronsUninstallReport struct {
	Repos       []cronsDisarmed  `json:"repos"`
	Removed     int              `json:"removed"`
	Remaining   int              `json:"remaining"`
	Timer       *crontimer.State `json:"timer,omitempty"`
	TimerDetail string           `json:"timer_detail,omitempty"`
}

type cronsDisarmed struct {
	Repo    string `json:"repo"`
	Removed int    `json:"removed"`
}

func renderCronsUninstall(report cronsUninstallReport, format string) error {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	for _, r := range report.Repos {
		fmt.Fprintf(os.Stdout, "disarmed %d schedule(s) in %s\n", r.Removed, r.Repo)
	}
	if len(report.Repos) == 0 || report.Removed == 0 {
		fmt.Fprintln(os.Stdout, "no schedules were armed here")
	}
	if report.TimerDetail != "" {
		fmt.Fprintf(os.Stdout, "timer: %s\n", report.TimerDetail)
	}
	return nil
}

// cronsTargetRoots names the checkouts a verb acts on: the enclosing one, the
// one --repo names, or every registered repo.
func cronsTargetRoots(repo string, fleet bool) ([]string, error) {
	if fleet {
		roots, err := fleetRepoRoots(runGit)
		if err != nil {
			return nil, err
		}
		if len(roots) == 0 {
			return nil, errors.New("no repos registered; run `sparkwing configure xrepo add <dir>` first")
		}
		return roots, nil
	}
	root, _, err := resolveHooksRepo(repo)
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}

// cronsProver proves a pipeline exists and compiles before its cadence is
// armed. A schedule fires unattended, so a pipeline that will not build is
// refused here rather than at three in the morning.
func cronsProver(noProve bool) func(repoRoot, pipeline string) error {
	if noProve {
		return nil
	}
	cache := map[string][]string{}
	return func(repoRoot, pipeline string) error {
		names, ok := cache[repoRoot]
		if !ok {
			var err error
			names, err = repos.PipelineNamesForRepo(repoRoot)
			if err != nil {
				return err
			}
			cache[repoRoot] = names
		}
		for _, n := range names {
			if n == pipeline {
				return nil
			}
		}
		return fmt.Errorf("the compiled pipeline binary does not name %q; `--no-prove` arms it anyway", pipeline)
	}
}
