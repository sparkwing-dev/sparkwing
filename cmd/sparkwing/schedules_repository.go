package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const scheduleOperationTimeout = 2 * time.Minute

type scheduleConnection struct {
	manager      controllerwire.ScheduleManager
	repositories controllerwire.ScheduleRepositoryManager
	close        func()
}

type scheduleCommand struct {
	connect     func(context.Context, string) (scheduleConnection, error)
	local       func(context.Context, string) (localScheduleServices, error)
	roots       func(bool, bool) ([]string, error)
	load        func(string) (*projectconfig.Config, error)
	source      func(context.Context, string, bool) (controllerwire.ScheduleRepoRequest, error)
	environment func() []string
	goos        string
}

func defaultScheduleCommand() scheduleCommand {
	return scheduleCommand{
		connect: connectSchedules, local: resolveLocalScheduleServices,
		roots: scheduleRoots, load: loadScheduleProject, source: readScheduleRepository, environment: os.Environ, goos: runtime.GOOS,
	}
}

func connectSchedules(ctx context.Context, profile string) (scheduleConnection, error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return scheduleConnection{}, err
	}
	manager, err := client.NewScheduleManager(options)
	if err != nil {
		closeTransport()
		return scheduleConnection{}, err
	}
	repositories, err := client.NewScheduleRepositoryManager(options)
	if err != nil {
		closeTransport()
		return scheduleConnection{}, err
	}
	return scheduleConnection{manager: manager, repositories: repositories, close: closeTransport}, nil
}

func validateScheduleScope(inv clisurface.Invocation, opts scheduleOptions) error {
	if inv.Value("profile") == "" {
		return nil
	}
	if inv.Bool("all-repos") {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--all-repos selects repositories on this host; --profile selects one repository on a controller"}
	}
	if opts.action == "pin" || opts.action == "unpin" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "controller schedule pins use schedules install --profile NAME; --unpinned follows the branch"}
	}
	return nil
}

func scheduleRoots(all, cleanup bool) ([]string, error) {
	if all {
		roots, err := fleetRepoRoots(runGit)
		if err == nil && len(roots) == 0 {
			err = fmt.Errorf("no repositories are registered; run sparkwing repos add --checkout DIR")
		}
		return roots, err
	}
	if cleanup {
		root, err := resolveCleanupRoot()
		return []string{root}, err
	}
	root, _, err := resolveHooksRepo()
	return []string{root}, err
}

func loadScheduleProject(root string) (*projectconfig.Config, error) {
	path := filepath.Join(root, ".sparkwing", projectconfig.Filename)
	cfg, err := projectconfig.Load(path)
	if err == nil && cfg == nil {
		err = fmt.Errorf("read %s: project file is missing", path)
	}
	return cfg, err
}

func selectScheduleDeclarations(cfg *projectconfig.Config, only []string, where string) ([]controllerwire.ScheduleDeclaration, []string, error) {
	wanted := map[string]bool{}
	for _, name := range only {
		name = strings.TrimSpace(name)
		if name != "" {
			wanted[name] = false
		}
	}
	selected, skipped := []controllerwire.ScheduleDeclaration{}, []string{}
	for _, pipeline := range cfg.Pipelines {
		if pipeline.On == nil {
			continue
		}
		for _, schedule := range pipeline.On.Schedule {
			name := "default"
			if schedule.Name != nil {
				name = *schedule.Name
			}
			selector := pipeline.Name + "/" + name
			matches := len(wanted) == 0
			for key := range wanted {
				if key == pipeline.Name || key == selector {
					matches, wanted[key] = true, true
				}
			}
			if !matches {
				continue
			}
			if schedule.Where != where {
				skipped = append(skipped, selector)
				continue
			}
			selected = append(selected, controllerwire.ScheduleDeclaration{
				Pipeline: pipeline.Name, Name: name,
				Cron: schedule.Cron, TZ: scheduleString(schedule.TZ), Overlap: scheduleString(schedule.Overlap),
				CatchUp: scheduleString(schedule.CatchUp), Args: maps.Clone(schedule.Args),
			})
		}
	}
	for key, found := range wanted {
		if !found {
			return nil, nil, fmt.Errorf("--schedule %s: repository declares no schedule by that name", key)
		}
	}
	return selected, skipped, nil
}

func scheduleString(value *string) string {
	if value != nil {
		return *value
	}
	return ""
}

func readScheduleRepository(ctx context.Context, root string, follow bool) (result controllerwire.ScheduleRepoRequest, resultErr error) {
	masker := secrets.NewMasker()
	diagnostics := &installDiagnostics{out: os.Stderr, stream: masker.Stream()}
	defer func() {
		resultErr = errors.Join(resultErr, diagnostics.close())
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{})
	if err != nil {
		return result, err
	}
	checkout := triggerCheckout{directory: root, environment: environment, diagnostics: diagnostics}
	remote, err := checkout.read(ctx, "remote", "get-url", "origin")
	if err != nil {
		return result, err
	}
	result.RepoURL, err = sourceurl.ValidateCloneURL(remote)
	if err != nil {
		return result, err
	}
	result.Branch, err = checkout.read(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil && !gitExitCode(err, 1) {
		return result, err
	}
	result.Follow = follow
	if follow {
		return result, nil
	}
	result.SHA, err = checkout.read(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return result, err
	}
	refs, err := checkout.read(ctx, "for-each-ref", "--count=1", "--contains="+result.SHA, "--format=%(refname)", "refs/remotes/origin/")
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(refs) == "" {
		return result, fmt.Errorf("selected commit is on no origin branch; push it first or use --unpinned")
	}
	dirty, err := checkout.read(ctx, "status", "--porcelain")
	if err != nil {
		return result, err
	}
	if dirty != "" {
		fmt.Fprintln(os.Stderr, "schedules install: uncommitted edits are excluded because the controller clones the selected commit")
	}
	return result, nil
}

type scheduleInstallation struct {
	Repo    string `json:"repo"`
	Profile string `json:"profile,omitempty"`
	controllerwire.ScheduleRepoWritten
	Skipped []string `json:"skipped"`
}

func installSchedules(ctx context.Context, inv clisurface.Invocation, opts scheduleOptions, out io.Writer, command scheduleCommand) error {
	if inv.Value("profile") == "" {
		return installLocalSchedules(ctx, inv, opts, out, command)
	}
	roots, err := command.roots(false, false)
	if err != nil {
		return err
	}
	cfg, err := command.load(roots[0])
	if err != nil {
		return err
	}
	declarations, skipped, err := selectScheduleDeclarations(cfg, inv.Flags["schedule"], "controller")
	if err != nil {
		return err
	}
	mode := scheduleWriteMode(inv)
	if mode == controllerwire.ScheduleUpsert && len(declarations) == 0 {
		return renderScheduleInstallation(out, scheduleInstallation{Repo: roots[0], Profile: inv.Value("profile"), Skipped: skipped}, opts.format)
	}
	input, err := command.source(ctx, roots[0], inv.Bool("unpinned"))
	if err != nil {
		return err
	}
	if input.Follow && input.Branch == "" {
		return fmt.Errorf("--unpinned requires a checked-out branch")
	}
	input.Schedules = declarations
	input.Mode = mode
	connection, err := command.connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer connection.close()
	result, err := connection.repositories.WriteSchedules(ctx, input)
	if err != nil {
		return err
	}
	return renderScheduleInstallation(out, scheduleInstallation{Repo: roots[0], Profile: inv.Value("profile"), ScheduleRepoWritten: result, Skipped: skipped}, opts.format)
}

func scheduleWriteMode(inv clisurface.Invocation) controllerwire.ScheduleWriteMode {
	if len(inv.Flags["schedule"]) > 0 {
		return controllerwire.ScheduleUpsert
	}
	return controllerwire.ScheduleReplace
}

func uninstallRepositorySchedules(ctx context.Context, inv clisurface.Invocation, opts scheduleOptions, out io.Writer, command scheduleCommand) error {
	roots, err := command.roots(false, true)
	if err != nil {
		return err
	}
	input, err := command.source(ctx, roots[0], true)
	if err != nil {
		return err
	}
	connection, err := command.connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer connection.close()
	result, err := connection.repositories.DeleteSchedulesForRepo(ctx, input.RepoURL)
	if err != nil {
		return err
	}
	if opts.format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if opts.format == "plain" {
		_, err = fmt.Fprintln(out, result.RepoURL)
	} else {
		_, err = fmt.Fprintf(out, "disarmed %d schedules for %s\n", result.Removed, result.RepoURL)
	}
	return err
}

func renderScheduleInstallation(out io.Writer, result scheduleInstallation, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	for _, row := range result.Schedules {
		text := row.ID
		if format == "pretty" {
			text = "armed " + row.Name + " (" + row.Lock.State + ")"
		}
		if _, err := fmt.Fprintln(out, text); err != nil {
			return err
		}
	}
	if format == "pretty" {
		for _, name := range result.Withdrawn {
			if _, err := fmt.Fprintln(out, "withdrawn "+name); err != nil {
				return err
			}
		}
		for _, name := range result.Skipped {
			if _, err := fmt.Fprintln(out, "skipped "+name+": its schedule runs elsewhere"); err != nil {
				return err
			}
		}
	}
	return nil
}

func scheduleUnavailable(dependency string) error {
	return &apierr.Error{Code: apierr.PolicyMissing, Message: "local schedules require " + dependency, Hint: "the local controller schedule binding is unavailable in this build"}
}
