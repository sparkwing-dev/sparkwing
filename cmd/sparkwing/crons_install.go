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

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type cronsInstallReport struct {
	Repos       []cronsRepoResult `json:"repos"`
	Armed       int               `json:"armed"`
	Refreshed   int               `json:"refreshed"`
	Withdrawn   int               `json:"withdrawn"`
	Controller  int               `json:"controller"`
	NothingToDo int               `json:"nothing_to_arm"`
	Timer       *crontimer.State  `json:"timer,omitempty"`
	TimerSkip   string            `json:"timer_skipped,omitempty"`
	TimerError  string            `json:"timer_error,omitempty"`
}

type cronsRepoResult struct {
	Repo        string       `json:"repo"`
	Schedules   []cronsArmed `json:"schedules,omitempty"`
	Controller  []string     `json:"controller,omitempty"`
	Withdrawals []string     `json:"withdrawals,omitempty"`
	Error       string       `json:"error,omitempty"`
}

type cronsArmed struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Lock   string `json:"lock"`
	Ref    string `json:"ref,omitempty"`
	Binary string `json:"binary,omitempty"`
}

func runCronsInstall(args []string) error {
	fs := flag.NewFlagSet(cmdCronsInstall.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	fleet := fs.Bool("fleet", false, "arm every registered repo")
	only := fs.StringSlice("only", nil, "arm only these pipelines or pipeline/name entries")
	follow := fs.Bool("follow", false, "arm without pinning, so each fire compiles the checkout")
	noProve := fs.Bool("no-prove", false, "arm without compiling the pipelines first, which also pins nothing")
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
	if *fleet && len(*only) > 0 {
		return errors.New("crons install: --only names entries of one repo; drop --fleet or drop --only")
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
	opts := crons.ArmOptions{Only: *only, Follow: *follow, Prove: cronsProver(*noProve)}
	report := cronsInstallReport{}
	var failed []string
	for _, root := range roots {
		result := cronsRepoResult{Repo: root}
		armed, aerr := session.svc.Arm(ctx, root, opts)
		switch {
		case aerr != nil:
			result.Error = aerr.Error()
			failed = append(failed, filepath.Base(root))
		case len(armed.Schedules) == 0 && armed.Withdrawn == 0 && len(armed.Controller) == 0:
			report.NothingToDo++
		default:
			for _, s := range armed.Schedules {
				result.Schedules = append(result.Schedules, describeArmed(s))
			}
			result.Controller = armed.Controller
			result.Withdrawals = armed.Withdrawals
			report.Armed += armed.Armed
			report.Refreshed += armed.Refreshed
			report.Withdrawn += armed.Withdrawn
			report.Controller += len(armed.Controller)
		}
		report.Repos = append(report.Repos, result)
	}

	switch terr := ensureCronsTimer(ctx, session, &report, *noTimer); {
	case errors.Is(terr, crontimer.ErrUnsupported):
		// safety: a platform with no timer of its own still armed everything,
		// so this is a hint about what to point at the tick, not a failure.
		report.TimerSkip = fmt.Sprintf("%v; the schedules are armed, so %s", terr, unsupportedTimerHint)
	case terr != nil:
		report.TimerError = terr.Error()
	}

	if err := renderCronsInstall(report, format); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("crons install: %s", strings.Join(failed, ", "))
	}
	if report.TimerError != "" {
		return fmt.Errorf("crons install: the schedules are armed but the timer is not: %s", report.TimerError)
	}
	return nil
}

func describeArmed(s store.CronSchedule) cronsArmed {
	armed := cronsArmed{
		ID:     s.ID,
		Name:   crons.DisplayName(s),
		Lock:   crons.LockFollows,
		Ref:    s.LockedRef,
		Binary: s.LockedBinary,
	}
	if s.LockedBinary != "" {
		armed.Lock = crons.LockPinned
	}
	return armed
}

func (a cronsArmed) describe() string {
	if a.Lock == crons.LockFollows {
		return "follows the checkout"
	}
	lock := crons.Lock{Ref: a.Ref}
	if a.Ref == "" {
		return "pinned to " + a.Binary
	}
	return "pinned at " + lock.ShortRef() + " -> " + a.Binary
}

// safety: timer trouble lands on the report, not the error, so the caller still renders what was armed.
func ensureCronsTimer(ctx context.Context, session *cronsSession, report *cronsInstallReport, skip bool) error {
	if skip {
		report.TimerSkip = "--no-timer: " + unsupportedTimerHint
		return nil
	}
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
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(report)
	case "plain":
		for _, r := range report.Repos {
			for _, s := range r.Schedules {
				fmt.Fprintln(os.Stdout, s.Name)
			}
		}
		return nil
	}
	multi := len(report.Repos) > 1
	for _, r := range report.Repos {
		if multi {
			fmt.Fprintf(os.Stdout, "\n=== %s\n", r.Repo)
		}
		switch {
		case r.Error != "":
			fmt.Fprintf(os.Stdout, "error: %s\n", r.Error)
		case len(r.Schedules) == 0 && len(r.Withdrawals) == 0 && len(r.Controller) == 0:
			if !multi {
				fmt.Fprintf(os.Stdout, "nothing to arm: no pipeline in %s declares a schedule\n", r.Repo)
			}
		default:
			for _, s := range r.Schedules {
				fmt.Fprintf(os.Stdout, "armed %s (%s)\n", s.Name, s.describe())
			}
			for _, name := range r.Controller {
				fmt.Fprintf(os.Stdout, "skipped %s: it fires from the controller, not from this host\n", name)
			}
			for _, name := range r.Withdrawals {
				fmt.Fprintf(os.Stdout, "withdrawn %s: the repo no longer declares it\n", name)
			}
		}
	}
	if multi {
		fmt.Fprintf(os.Stdout, "\n%d schedule(s) armed, %d refreshed, %d withdrawn, %d for the controller, %d repo(s) with nothing to arm\n",
			report.Armed, report.Refreshed, report.Withdrawn, report.Controller, report.NothingToDo)
	}
	switch {
	case report.TimerError != "":
		fmt.Fprintf(os.Stdout, "timer: %s\n", report.TimerError)
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
	rows, err := session.svc.List(ctx)
	if err != nil {
		return fmt.Errorf("crons uninstall: %w", err)
	}
	namesByRepo := map[string][]string{}
	var armedRoots []string
	for _, r := range rows {
		if _, seen := namesByRepo[r.RepoPath]; !seen {
			armedRoots = append(armedRoots, r.RepoPath)
		}
		namesByRepo[r.RepoPath] = append(namesByRepo[r.RepoPath], r.Display)
	}
	roots := armedRoots
	if !*fleet {
		roots, err = cronsTargetRoots(*repo, false)
		if err != nil {
			return fmt.Errorf("crons uninstall: %w", err)
		}
	}

	report := cronsUninstallReport{}
	for _, root := range roots {
		removed, derr := session.svc.DisarmRepo(ctx, root)
		if derr != nil {
			return fmt.Errorf("crons uninstall: %w", derr)
		}
		report.Repos = append(report.Repos, cronsDisarmed{Repo: root, Removed: removed, Schedules: namesByRepo[root]})
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
	Repo      string   `json:"repo"`
	Removed   int      `json:"removed"`
	Schedules []string `json:"schedules,omitempty"`
}

func renderCronsUninstall(report cronsUninstallReport, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(report)
	case "plain":
		for _, r := range report.Repos {
			for _, name := range r.Schedules {
				fmt.Fprintln(os.Stdout, name)
			}
		}
		return nil
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

// safety: a schedule fires unattended, so a pipeline that will not build is
// refused at arm time rather than at three in the morning, and the binary that
// proved it is what the arm pins. Tests replace this with a stub compiler.
var cronsProver = func(noProve bool) crons.Prover {
	if noProve {
		return nil
	}
	names := map[string][]string{}
	return func(ctx context.Context, repoRoot, pipeline string) (crons.Proof, error) {
		proof, err := compileRepoPipelines(ctx, repoRoot)
		if err != nil {
			return crons.Proof{}, err
		}
		declared, ok := names[repoRoot]
		if !ok {
			if declared, err = repos.PipelineNamesForRepo(repoRoot); err != nil {
				return crons.Proof{}, err
			}
			names[repoRoot] = declared
		}
		for _, n := range declared {
			if n == pipeline {
				return proof, nil
			}
		}
		return crons.Proof{}, fmt.Errorf(
			"the compiled pipeline binary does not name %q; `--no-prove` arms it anyway", pipeline)
	}
}

// safety: the cache lease is released before the arm copies the file, so a
// prune landing between the two fails the arm on the missing path rather than
// pinning a partial binary.
func compileRepoPipelines(ctx context.Context, repoRoot string) (crons.Proof, error) {
	sparkwingDir := filepath.Join(repoRoot, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return crons.Proof{}, fmt.Errorf("no .sparkwing/ at %s: %w", sparkwingDir, err)
	}
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return crons.Proof{}, fmt.Errorf("hash %s: %w", sparkwingDir, err)
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return crons.Proof{}, fmt.Errorf("cache entry: %w", err)
	}
	lease, _, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		return bincache.CompilePipeline(ctx, sparkwingDir, tempPath)
	})
	if err != nil {
		return crons.Proof{}, fmt.Errorf("compile %s: %w", sparkwingDir, err)
	}
	path := lease.Path()
	if rerr := lease.Release(); rerr != nil {
		return crons.Proof{}, fmt.Errorf("release the pipeline cache lease: %w", rerr)
	}
	return crons.Proof{Binary: path, Digest: key}, nil
}
