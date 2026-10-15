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
	tenant, err := st.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		release()
		return nil, nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	session := &cronsSession{
		svc: &crons.Service{
			Store:     st,
			Schedules: tenant,
			Launcher:  cronsLauncher(st, paths),
			Now:       clock,
			Host:      host,
			Version:   installedVersion(),
			LockPath:  filepath.Join(paths.Root, cronsLockFile),
			PinRoot:   filepath.Join(paths.Root, cronsPinDir),
			ArmedBy:   armedBy(host),
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
		Pipeline: s.Pipeline,
		RepoDir:  s.RepoPath,
		Gate: riskGate{
			Surface:  "schedule " + s.ID,
			Pipeline: s.Pipeline,
		}.check,
		Source:       scheduleTriggerSource,
		ScheduleID:   s.ID,
		Args:         s.Effective().Args,
		PinnedBinary: s.LockedBinary,
		PinnedDigest: s.LockedDigest,
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

func (l cronLauncher) Active(ctx context.Context, _ store.CronSchedule, runID string, staleAfter time.Duration) (bool, error) {
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
	env := map[string]string{}
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
		// safety: the unit names the home explicitly, so the tick evaluates the
		// store that armed it wherever the service manager's environment points.
		SparkwingHome: paths.Root,
		LogPath:       filepath.Join(paths.Root, cronsLogFile),
		UID:           os.Getuid(),
		Exec:          crontimer.DefaultExec,
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

const unsupportedTimerHint = "run `sparkwing __tick` from any scheduler on this machine, once a minute"

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
