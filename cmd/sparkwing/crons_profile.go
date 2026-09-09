package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: a push resolves a repository, seeds the gitcache and writes rows, so
// it gets more room than a read of the same controller.
const cronsProfileTimeout = 2 * time.Minute

func addCronsProfileFlag(fs *flag.FlagSet) *string {
	return fs.String("profile", "", "profile name; omit for this host")
}

// safety: the profile name rides beside the client so every message names the
// controller that answered.
type cronsRemote struct {
	name string
	prof *profile.Profile
	api  *client.Client
}

func openCronsRemote(profileName, verb string) (*cronsRemote, error) {
	prof, err := resolveProfile(profileName)
	if err != nil {
		return nil, err
	}
	if err := requireController(prof, verb); err != nil {
		return nil, err
	}
	return &cronsRemote{
		name: prof.Name,
		prof: prof,
		api:  client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()),
	}, nil
}

func cronsRemoteContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cronsProfileTimeout)
}

func (r *cronsRemote) rows(ctx context.Context) ([]crons.Row, crons.Health, error) {
	overview, err := r.api.ListCrons(ctx)
	if err != nil {
		return nil, crons.Health{}, err
	}
	rows := make([]crons.Row, 0, len(overview.Schedules))
	for _, view := range overview.Schedules {
		rows = append(rows, crons.RowFromView(view))
	}
	return rows, crons.HealthFromView(overview.Health), nil
}

func runCronsStatusProfile(profileName, format string) error {
	remote, err := openCronsRemote(profileName, "crons status")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	_, health, err := remote.rows(ctx)
	if err != nil {
		return fmt.Errorf("crons status: %s: %w", remote.name, err)
	}
	if err := renderCronsHealth(os.Stdout, health, time.Now(), format); err != nil {
		return err
	}
	if !health.Healthy() {
		return exitErrorf(1, "crons status: %s: %s", remote.name, health.Detail)
	}
	return nil
}

func runCronsListProfile(profileName, format string, all bool) error {
	remote, err := openCronsRemote(profileName, "crons list")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	rows, _, err := remote.rows(ctx)
	if err != nil {
		return fmt.Errorf("crons list: %s: %w", remote.name, err)
	}
	shown, hidden := rows, 0
	if !all {
		shown = shown[:0:0]
		for _, row := range rows {
			if row.State == crons.StateUndeclared {
				hidden++
				continue
			}
			shown = append(shown, row)
		}
	}
	return renderCronsList(os.Stdout, shown, hidden, time.Now(), format)
}

func runCronsShowProfile(profileName, name, format string, fires int) error {
	remote, err := openCronsRemote(profileName, "crons show")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	detail, err := remote.api.GetCron(ctx, name)
	if err != nil {
		return fmt.Errorf("crons show: %s: %w", remote.name, err)
	}
	report := cronsShowReport{Row: crons.RowFromView(detail.Schedule)}
	for i, fire := range detail.Fires {
		if fires > 0 && i >= fires {
			break
		}
		report.Fires = append(report.Fires, cronsFireView{
			CronFire: crons.FireFromView(fire), RunStatus: fire.RunStatus,
		})
	}
	return renderCronsShow(os.Stdout, report, format)
}

func runCronsNextProfile(profileName, name, format string, count int) error {
	remote, err := openCronsRemote(profileName, "crons next")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	var wanted []crons.Row
	if name != "" {
		detail, derr := remote.api.GetCron(ctx, name)
		if derr != nil {
			return fmt.Errorf("crons next: %s: %w", remote.name, derr)
		}
		wanted = []crons.Row{crons.RowFromView(detail.Schedule)}
	} else {
		rows, _, lerr := remote.rows(ctx)
		if lerr != nil {
			return fmt.Errorf("crons next: %s: %w", remote.name, lerr)
		}
		for _, row := range rows {
			if row.State == crons.StateArmed {
				wanted = append(wanted, row)
			}
		}
	}
	now := time.Now()
	var out []cronsUpcoming
	for _, row := range wanted {
		instants, uerr := crons.UpcomingAfter(row.Effective.Cron, row.Effective.TZ, now, count)
		if uerr != nil {
			return fmt.Errorf("crons next: %s: %w", row.Display, uerr)
		}
		for _, at := range instants {
			out = append(out, cronsUpcoming{Schedule: row.ID, Name: row.Display, At: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > count {
		out = out[:count]
	}
	return renderCronsNext(os.Stdout, out, now, format)
}

func runCronsPauseResumeProfile(profileName, name, format string, pause bool) error {
	verb := "resume"
	if pause {
		verb = "pause"
	}
	remote, err := openCronsRemote(profileName, "crons "+verb)
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	var view *crons.ScheduleView
	if pause {
		view, err = remote.api.PauseCron(ctx, name)
	} else {
		view, err = remote.api.ResumeCron(ctx, name)
	}
	if err != nil {
		return fmt.Errorf("crons %s: %s: %w", verb, remote.name, err)
	}
	row := crons.RowFromView(*view)
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(row)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, row.ID)
		return perr
	}
	fmt.Fprintf(os.Stdout, "%s is %s\n", row.Display, row.State)
	if !pause && row.NextDueAt != nil {
		fmt.Fprintf(os.Stdout, "  next: %s\n", cronAbsTime(*row.NextDueAt, row.Location))
	}
	return nil
}

func runCronsRunProfile(profileName, name, format string) error {
	remote, err := openCronsRemote(profileName, "crons run")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	launched, err := remote.api.RunCronNow(ctx, name)
	if err != nil {
		return fmt.Errorf("crons run: %s: %w", remote.name, err)
	}
	report := cronsRunReport{
		Schedule: launched.Schedule.ID,
		Name:     launched.Schedule.Name,
		Pipeline: launched.Schedule.Pipeline,
		RunID:    launched.RunID,
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(report)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, report.RunID)
		return perr
	}
	fmt.Fprintf(os.Stdout, "run %s submitted on %s (%s)\n", report.RunID, remote.name, report.Pipeline)
	fmt.Fprintf(os.Stdout, "  follow: sparkwing runs logs --run %s --profile %s --follow\n", report.RunID, remote.name)
	return nil
}

func runCronsDisarmProfile(profileName, name, format string) error {
	remote, err := openCronsRemote(profileName, "crons disarm")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	view, err := remote.api.DisarmCron(ctx, name)
	if err != nil {
		return fmt.Errorf("crons disarm: %s: %w", remote.name, err)
	}
	report := cronsDisarmReport{Schedule: view.ID, Name: view.Name}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(report)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, report.Name)
		return perr
	}
	fmt.Fprintf(os.Stdout, "disarmed %s on %s; its history went with it\n", report.Name, remote.name)
	return nil
}

func runCronsSetProfile(profileName, name, format string, req client.CronOverrideRequest) error {
	remote, err := openCronsRemote(profileName, "crons set")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	view, err := remote.api.SetCronOverride(ctx, name, req)
	if err != nil {
		return fmt.Errorf("crons set: %s: %w", remote.name, err)
	}
	return renderCronsOverride(crons.RowFromView(*view), format)
}

func runCronsResetProfile(profileName, name, format string) error {
	remote, err := openCronsRemote(profileName, "crons reset")
	if err != nil {
		return err
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	view, err := remote.api.ClearCronOverride(ctx, name)
	if err != nil {
		return fmt.Errorf("crons reset: %s: %w", remote.name, err)
	}
	return renderCronsOverride(crons.RowFromView(*view), format)
}

// safety: a controller schedule's pin is the commit it clones, which moves only
// when the repository is pushed again, so there is no separate lock to take.
func cronsRemotePinError(verb string) error {
	return fmt.Errorf(
		"crons %s: a controller schedule is pinned by the commit it was pushed at; "+
			"`sparkwing crons install --profile <name>` re-pins it at HEAD, and `--follow` makes it clone the branch tip",
		verb)
}

func runCronsInstallProfile(profileName, root string, only []string, follow bool, format string) error {
	remote, err := openCronsRemote(profileName, "crons install")
	if err != nil {
		return err
	}
	declared, err := crons.DeclaredSchedules(root)
	if err != nil {
		return fmt.Errorf("crons install: %w", err)
	}
	selected, err := selectDeclaredForPush(declared, only, root)
	if err != nil {
		return fmt.Errorf("crons install: %w", err)
	}
	report := cronsPushReport{Repo: root, Profile: remote.name}
	var push []client.CronRepoSchedule
	for _, d := range selected {
		if d.Local() {
			report.Local = append(report.Local, d.DisplayName())
			continue
		}
		push = append(push, client.CronRepoSchedule{
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
		return fmt.Errorf("crons install: %s has no git origin. "+
			"The cluster clones the pipeline source at each fire, so the repository needs one", root)
	}
	if repoSlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repoSlug)
	} else if repoURL, err = sourceurl.ValidateCloneURL(repoURL); err != nil {
		return fmt.Errorf("crons install: %s has an origin the cluster cannot clone: %w", root, err)
	}
	report.RepoURL = repoURL
	report.Branch = branch
	if !follow {
		report.SHA = sha
	}

	if len(push) > 0 && sha != "" {
		seedCronsSource(remote.prof, root, repoURL, sha)
	}

	resp, err := remote.api.PutCronRepo(context.Background(), client.CronRepoRequest{
		RepoURL:   repoURL,
		Branch:    branch,
		SHA:       sha,
		Follow:    follow,
		Schedules: push,
	})
	if err != nil {
		return fmt.Errorf("crons install: %s: %w", remote.name, err)
	}
	for _, view := range resp.Schedules {
		report.Schedules = append(report.Schedules, cronsPushed{
			ID: view.ID, Name: view.Name, Ref: view.Lock.Ref, State: view.StateDetail,
		})
	}
	report.Withdrawals = resp.Withdrawn
	return renderCronsPush(os.Stdout, report, format)
}

// safety: the same selection `crons install` applies on a host, so a name the
// repository does not declare is refused before anything is pushed.
func selectDeclaredForPush(declared []crons.Declared, only []string, root string) ([]crons.Declared, error) {
	if len(only) == 0 {
		return declared, nil
	}
	var out []crons.Declared
	for _, want := range only {
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

// safety: a seed that fails is a warning, not a failure: the cluster's trigger
// loop fetches the commit itself when it finds the cache short.
func seedCronsSource(prof *profile.Profile, repoDir, repoURL, sha string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	services, derr := discovery.ServicesFor(ctx, prof.ControllerURL(), prof.ControllerToken())
	cancel()
	seedTriggerSource(prof, services.CachePod, derr, repoDir, repoURL, sha)
}

func runCronsUninstallProfile(profileName, root, format string) error {
	remote, err := openCronsRemote(profileName, "crons uninstall")
	if err != nil {
		return err
	}
	_, _, repoSlug, repoURL := gitContextIn(root)
	if repoURL == "" {
		return fmt.Errorf("crons uninstall: %s has no git origin, so its pushed schedules cannot be named", root)
	}
	if repoSlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repoSlug)
	} else if repoURL, err = sourceurl.ValidateCloneURL(repoURL); err != nil {
		return fmt.Errorf("crons uninstall: %s has an origin the controller cannot name: %w", root, err)
	}
	ctx, cancel := cronsRemoteContext()
	defer cancel()
	resp, err := remote.api.DeleteCronRepo(ctx, repoURL)
	if err != nil {
		return fmt.Errorf("crons uninstall: %s: %w", remote.name, err)
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
	Profile     string        `json:"profile"`
	Repo        string        `json:"repo"`
	RepoURL     string        `json:"repo_url"`
	Branch      string        `json:"branch,omitempty"`
	SHA         string        `json:"sha,omitempty"`
	Schedules   []cronsPushed `json:"schedules,omitempty"`
	Local       []string      `json:"local,omitempty"`
	Withdrawals []string      `json:"withdrawals,omitempty"`
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
		return ndjson.Write(w, report.Schedules)
	}
	if len(report.Schedules) == 0 && len(report.Withdrawals) == 0 {
		fmt.Fprintf(w, "nothing to push: no pipeline in %s declares a `where: controller` schedule\n", report.Repo)
	}
	for _, s := range report.Schedules {
		fmt.Fprintf(w, "pushed %s to %s (%s)\n", s.Name, report.Profile, dashIfEmpty(s.State))
	}
	for _, name := range report.Local {
		fmt.Fprintf(w, "skipped %s: it fires from this host, so `sparkwing crons install` arms it here\n", name)
	}
	for _, name := range report.Withdrawals {
		fmt.Fprintf(w, "withdrawn %s: the repo no longer declares it for the controller\n", name)
	}
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
	"--fleet arms every repo registered on this host; name one repo with --repo when pushing to a controller")

// safety: the same edit the local path applies, spelled for the wire: a field
// the operator did not name is left out, and args ride whole.
func cronsOverrideRequest(fields crons.Override) client.CronOverrideRequest {
	req := client.CronOverrideRequest{Args: fields.Args}
	if fields.Cron != nil {
		req.Cron = *fields.Cron
	}
	if fields.TZ != nil {
		req.TZ = *fields.TZ
	}
	if fields.Overlap != nil {
		req.Overlap = *fields.Overlap
	}
	if fields.CatchUp != nil {
		req.CatchUp = fields.CatchUp.String()
	}
	return req
}
