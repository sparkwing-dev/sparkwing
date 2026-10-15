package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: a push resolves a repository and writes rows, so
// it gets more room than a read of the same controller.
const cronsProfileTimeout = 2 * time.Minute

func addCronsProfileFlag(fs *flag.FlagSet) *string {
	return fs.String("profile", "", "profile name; omit for this host")
}

// safety: the profile name rides beside the client so every message names the
// controller that answered.
type cronsRemote struct {
	name  string
	api   controllerwire.ScheduleRepositoryManager
	close func()
}

func openCronsRemote(profileName, _ string) (*cronsRemote, error) {
	options, closeTransport, err := controllerOptions(context.Background(), profileName)
	if err != nil {
		return nil, err
	}
	manager, err := client.NewScheduleRepositoryManager(options)
	if err != nil {
		closeTransport()
		return nil, err
	}
	return &cronsRemote{name: profileName, api: manager, close: closeTransport}, nil
}

func cronsRemoteContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cronsProfileTimeout)
}

// safety: a controller schedule's pin is the commit it clones, which moves only
// when the repository is pushed again, so there is no separate lock to take.
func cronsRemotePinError(flagName string) error {
	return fmt.Errorf(
		"schedules set %s: a controller schedule is pinned by the commit it was pushed at; "+
			"`sparkwing schedules install --profile <name>` re-pins it at HEAD, and `--unpinned` makes it clone the branch tip",
		flagName)
}

func runCronsInstallProfile(profileName, root string, only []string, follow bool, format string) error {
	remote, err := openCronsRemote(profileName, "schedules install")
	if err != nil {
		return err
	}
	defer remote.close()
	declared, err := crons.DeclaredSchedules(root)
	if err != nil {
		return fmt.Errorf("schedules install: %w", err)
	}
	selected, err := selectDeclaredForPush(declared, only, root)
	if err != nil {
		return fmt.Errorf("schedules install: %w", err)
	}
	report := cronsPushReport{Repo: root, Profile: remote.name}
	var push []controllerwire.ScheduleDeclaration
	for _, d := range selected {
		if d.Local() {
			report.Local = append(report.Local, d.DisplayName())
			continue
		}
		push = append(push, controllerwire.ScheduleDeclaration{
			Pipeline: d.Pipeline,
			Name:     d.Name,
			Cron:     d.Trigger.Cron,
			TZ:       d.Trigger.TZ,
			Overlap:  d.Trigger.Overlap,
			CatchUp:  d.Trigger.CatchUp,
			Args:     d.Trigger.Args,
		})
	}

	branch, sha, repoSlug, repoURL := gitContextIn(root)
	if repoURL == "" {
		return fmt.Errorf("schedules install: %s has no git origin. "+
			"The cluster clones the pipeline source at each fire, so the repository needs one", root)
	}
	if repoSlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repoSlug)
	} else if repoURL, err = sourceurl.ValidateCloneURL(repoURL); err != nil {
		return fmt.Errorf("schedules install: %s has an origin the cluster cannot clone: %w", root, err)
	}
	if len(push) > 0 {
		if err := checkPushableHead(root, follow); err != nil {
			return err
		}
	}
	report.RepoURL = repoURL
	report.Branch = branch
	if !follow {
		report.SHA = sha
	}

	ctx, cancel := cronsRemoteContext()
	defer cancel()
	resp, err := remote.api.ReplaceSchedules(ctx, controllerwire.ScheduleRepoRequest{
		RepoURL:   repoURL,
		Branch:    branch,
		SHA:       sha,
		Follow:    follow,
		Schedules: push,
	})
	if err != nil {
		return fmt.Errorf("schedules install: %s: %w", remote.name, err)
	}
	for _, view := range resp.Schedules {
		report.Schedules = append(report.Schedules, cronsPushed{
			ID: view.ID, Name: view.Name, Ref: view.Lock.Ref, State: view.StateDetail,
		})
		if row := crons.RowFromView(view); row.CatchUpClamped {
			report.Clamped = append(report.Clamped, cronsCatchUpCap{
				Name:      row.Display,
				Declared:  row.CronSchedule.Effective().CatchUp.String(),
				Effective: row.Effective.CatchUp.String(),
			})
		}
	}
	report.Withdrawals = resp.Withdrawn
	return renderCronsPush(os.Stdout, report, format)
}

// safety: the controller clones what it is pushed, so a commit no remote branch
// carries makes every fire fail at the clone. The check is skipped under
// --follow, which pins the branch tip rather than this checkout's HEAD.
func checkPushableHead(root string, follow bool) error {
	if follow {
		return nil
	}
	if dirty, known := gitWorkingTreeDirty(root); known && dirty {
		fmt.Fprintf(os.Stderr,
			"schedules install: %s has uncommitted edits; the controller clones the pushed commit, "+
				"so they are not part of what fires\n", root)
	}
	_, sha, _, _ := gitContextIn(root)
	if commitOnOrigin(root, sha) {
		return nil
	}
	return fmt.Errorf(
		"schedules install: %s is on a commit no origin branch carries, so every fire would fail at the clone. "+
			"Push the branch first, or use --follow to clone the tip of %s at each fire instead",
		root, dashIfEmpty(gitBranchName(root)))
}

func gitWorkingTreeDirty(root string) (dirty, known bool) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(out)) != "", true
}

func gitBranchName(root string) string {
	branch, _, _, _ := gitContextIn(root)
	return branch
}

// safety: the same selection `schedules install` applies on a host, so a name the
// repository does not declare is refused before anything is pushed.
func selectDeclaredForPush(declared []crons.Declared, only []string, root string) ([]crons.Declared, error) {
	if len(only) == 0 {
		return declared, nil
	}
	var out []crons.Declared
	for _, want := range only {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		var matched []crons.Declared
		for _, d := range declared {
			if d.Pipeline == want || d.Selector() == want {
				matched = append(matched, d)
			}
		}
		if len(matched) == 0 {
			return nil, fmt.Errorf("--only %s: %s declares no schedule by that name", want, root)
		}
		out = append(out, matched...)
	}
	return out, nil
}

func runCronsUninstallProfile(profileName, root, format string) error {
	remote, err := openCronsRemote(profileName, "schedules uninstall")
	if err != nil {
		return err
	}
	defer remote.close()
	_, _, repoSlug, repoURL := gitContextIn(root)
	if repoURL == "" {
		return fmt.Errorf("schedules uninstall: %s has no git origin, so its pushed schedules cannot be named", root)
	}
	if repoSlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repoSlug)
	} else if repoURL, err = sourceurl.ValidateCloneURL(repoURL); err != nil {
		return fmt.Errorf("schedules uninstall: %s has an origin the controller cannot name: %w", root, err)
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	resp, err := remote.api.DeleteSchedulesForRepo(ctx, repoURL)
	if err != nil {
		return fmt.Errorf("schedules uninstall: %s: %w", remote.name, err)
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(resp)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, resp.RepoURL)
		return perr
	}
	fmt.Fprintf(os.Stdout, "disarmed %d schedule(s) for %s on %s\n", resp.Removed, resp.RepoURL, remote.name)
	return nil
}

type cronsPushReport struct {
	Profile     string            `json:"profile"`
	Repo        string            `json:"repo"`
	RepoURL     string            `json:"repo_url"`
	Branch      string            `json:"branch,omitempty"`
	SHA         string            `json:"sha,omitempty"`
	Schedules   []cronsPushed     `json:"schedules,omitempty"`
	Local       []string          `json:"local,omitempty"`
	Withdrawals []string          `json:"withdrawals,omitempty"`
	Clamped     []cronsCatchUpCap `json:"clamped,omitempty"`
}

type cronsPushed struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Ref   string `json:"ref,omitempty"`
	State string `json:"state,omitempty"`
}

func renderCronsPush(w io.Writer, report cronsPushReport, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(report)
	case "plain":
		for _, s := range report.Schedules {
			if _, err := fmt.Fprintln(w, s.Name); err != nil {
				return err
			}
		}
		return nil
	}
	if len(report.Schedules) == 0 && len(report.Withdrawals) == 0 {
		fmt.Fprintf(w, "nothing to push: no pipeline in %s declares a `where: controller` schedule\n", report.Repo)
	}
	for _, s := range report.Schedules {
		fmt.Fprintf(w, "pushed %s to %s (%s)\n", s.Name, report.Profile, dashIfEmpty(s.State))
	}
	for _, name := range report.Local {
		fmt.Fprintf(w, "skipped %s: it fires from this host, so `sparkwing schedules install` arms it here\n", name)
	}
	for _, name := range report.Withdrawals {
		fmt.Fprintf(w, "withdrawn %s: the repo no longer declares it for the controller\n", name)
	}
	renderCronsClamped(w, report.Clamped)
	if len(report.Schedules) > 0 {
		fmt.Fprintf(w, "source %s\n", report.RepoURL)
		if report.SHA == "" {
			fmt.Fprintf(w, "  each fire clones the tip of %s\n", dashIfEmpty(report.Branch))
		} else {
			fmt.Fprintf(w, "  each fire clones %s at %s\n", dashIfEmpty(report.Branch), report.SHA)
		}
	}
	return nil
}

var errCronsProfileAndFleet = errors.New(
	"--fleet arms every repo registered on this host; run it from one repo (or pass -C DIR) when pushing to a controller")
