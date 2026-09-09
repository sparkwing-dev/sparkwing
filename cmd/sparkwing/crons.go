package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the bare word, no host suffix, because pipelines branch on it.
const scheduleTriggerSource = "schedule"

const cronsLogFile = "crons.log"

const cronsLockFile = "crons.lock"

const cronsPinDir = "crons"

func runCrons(args []string) error {
	if handleParentHelp(cmdCrons, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCrons, os.Stderr)
		return errors.New("crons: subcommand required " +
			"(install|uninstall|disarm|lock|unlock|set|reset|status|list|show|next|pause|resume|run|tick)")
	}
	switch args[0] {
	case "install":
		return runCronsInstall(args[1:])
	case "uninstall":
		return runCronsUninstall(args[1:])
	case "disarm":
		return runCronsDisarm(args[1:])
	case "lock":
		return runCronsLock(args[1:])
	case "unlock":
		return runCronsUnlock(args[1:])
	case "set":
		return runCronsSet(args[1:])
	case "reset":
		return runCronsReset(args[1:])
	case "status":
		return runCronsStatus(args[1:])
	case "list":
		return runCronsList(args[1:])
	case "show":
		return runCronsShow(args[1:])
	case "next":
		return runCronsNext(args[1:])
	case "pause":
		return runCronsPause(args[1:])
	case "resume":
		return runCronsResume(args[1:])
	case "run":
		return runCronsRun(args[1:])
	case "tick":
		return runCronsTick(args[1:])
	default:
		PrintHelp(cmdCrons, os.Stderr)
		return fmt.Errorf("crons: unknown subcommand %q", args[0])
	}
}

type cronsSession struct {
	svc   *crons.Service
	store *store.Store
	paths orchestrator.Paths
}

func openCrons(nowRFC string) (*cronsSession, func(), error) {
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return nil, nil, err
	}
	if err := paths.EnsureRoot(); err != nil {
		return nil, nil, fmt.Errorf("ensure %s: %w", paths.Root, err)
	}
	clock, err := cronsClock(nowRFC)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", paths.StateDB(), err)
	}
	release := func() {
		if cerr := st.Close(); cerr != nil {
			slog.Default().Warn("close runs store", "path", paths.StateDB(), "error", cerr)
		}
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	session := &cronsSession{
		svc: &crons.Service{
			Store:    st,
			Launcher: cronsLauncher(st, paths),
			Now:      clock,
			Host:     host,
			Version:  installedVersion(),
			LockPath: filepath.Join(paths.Root, cronsLockFile),
			PinRoot:  filepath.Join(paths.Root, cronsPinDir),
			ArmedBy:  armedBy(host),
		},
		store: st,
		paths: paths,
	}
	return session, release, nil
}

func cronsClock(nowRFC string) (func() time.Time, error) {
	if nowRFC == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339, nowRFC)
	if err != nil {
		return nil, fmt.Errorf("--sw-now %q: expected an RFC3339 instant such as 2026-01-01T03:00:00Z", nowRFC)
	}
	return func() time.Time { return at }, nil
}

func armedBy(host string) string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	switch {
	case name == "" && host == "":
		return ""
	case host == "":
		return name
	case name == "":
		return host
	}
	return name + "@" + host
}

func (s *cronsSession) now() time.Time {
	if s.svc.Now != nil {
		return s.svc.Now()
	}
	return time.Now()
}

// safety: tests replace this to record launches instead of starting runs.
var cronsLauncher = func(st *store.Store, paths orchestrator.Paths) crons.Launcher {
	return cronLauncher{store: st, paths: paths}
}

type cronLauncher struct {
	store *store.Store
	paths orchestrator.Paths
}

func (l cronLauncher) Launch(ctx context.Context, s store.CronSchedule, _ time.Time) (string, error) {
	// safety: the effective arguments are passed as a submission's own, exactly
	// as `sparkwing run <pipeline> --key value` would, so the repository's
	// defaults and the pipeline's own args still merge underneath them.
	result, err := persistSubmission(ctx, l.store, l.paths, submission{
		Pipeline:     s.Pipeline,
		RepoDir:      s.RepoPath,
		Source:       scheduleTriggerSource,
		ScheduleID:   s.ID,
		Args:         s.Effective().Args,
		PinnedBinary: s.LockedBinary,
	})
	if err != nil {
		return "", err
	}
	if cerr := cronsEnsureConsumer(l.paths.Root); cerr != nil {
		return "", fmt.Errorf("run %s stays queued and will start when a consumer does, but none could be started now: %w",
			result.RunID, cerr)
	}
	return result.RunID, nil
}

func (l cronLauncher) Active(ctx context.Context, runID string, staleAfter time.Duration) (bool, error) {
	if _, err := orchestrator.ReconcileOrphanedLocalRuns(ctx, l.store, 0); err != nil {
		return false, err
	}
	run, err := l.store.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if run == nil {
		return false, nil
	}
	switch run.Status {
	case "running":
		return true, nil
	case "pending":
		// safety: nothing reconciles a run no consumer ever claimed, so a skip
		// policy would read it as active for ever and never fire again.
		return time.Since(runQueuedAt(run)) <= staleAfter, nil
	}
	return false, nil
}

// safety: CreatedAt is the trigger-intake stamp a detached submission writes;
// StartedAt is all a row from before that column has.
func runQueuedAt(run *store.Run) time.Time {
	if !run.CreatedAt.IsZero() {
		return run.CreatedAt
	}
	return run.StartedAt
}

// safety: tests replace this to exercise a launch with no consumer, without
// spawning one.
var cronsEnsureConsumer = func(home string) error { return ensureTriggerConsumer(home, 0, 0) }

// safety: tests replace this with a fake service manager and unit directory.
var cronsTimerHost = defaultCronsTimerHost

func defaultCronsTimerHost(paths orchestrator.Paths) (crontimer.Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return crontimer.Host{}, fmt.Errorf("resolve the home directory the timer is installed under: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	binary, err := cronsTimerBinary()
	if err != nil {
		return crontimer.Host{}, err
	}
	// safety: the unit names the home explicitly, so the tick evaluates the
	// store that armed it wherever the service manager's environment points.
	env := map[string]string{"SPARKWING_HOME": paths.Root}
	// safety: the daemon host is resolved from PATH at run time, so a
	// side-by-side build (SPARKWING_INSTALL_NAME in bin/install.sh) names its
	// own daemon here or its scheduled runs meet the released daemon and are
	// refused.
	if v := os.Getenv(wingdclient.HostBinEnv); v != "" {
		env[wingdclient.HostBinEnv] = v
	}
	return crontimer.Host{
		GOOS:       runtime.GOOS,
		Home:       home,
		ConfigHome: configHome,
		Binary:     binary,
		PathEnv:    os.Getenv("PATH"),
		Env:        env,
		LogPath:    filepath.Join(paths.Root, cronsLogFile),
		UID:        os.Getuid(),
		Exec:       crontimer.DefaultExec,
	}, nil
}

// safety: a systemd or launchd job inherits no PATH, so the absolute path is baked in.
func cronsTimerBinary() (string, error) {
	if self, err := installsite.Self(); err == nil && self != "" {
		return self, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve this sparkwing binary for the timer to run: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

const unsupportedTimerHint = "run `sparkwing crons tick` from any scheduler on this machine, once a minute"

func cronsOutputFlag(fs *flag.FlagSet) *string {
	return fs.StringP("output", "o", "", "output format: pretty|json|plain")
}

func cronsNowFlag(fs *flag.FlagSet) *string {
	now := fs.String("sw-now", "", "evaluate as if it were this RFC3339 instant")
	if err := fs.MarkHidden("sw-now"); err != nil {
		panic(err)
	}
	return now
}

// safety: reads against the tick's clock, so --sw-now renders deterministically.
func cronRelTime(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	if t.After(now) {
		return "in " + shortDuration(t.Sub(now))
	}
	return shortDuration(now.Sub(t)) + " ago"
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func cronAbsTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04:05 MST")
}
