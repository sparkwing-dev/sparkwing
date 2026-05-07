// `sparkwing dashboard {start,kill,status}` -- background lifecycle
// for the in-process dashboard + API + logs server (pkg/localws).
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/localws"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage/storeurl"
)

const (
	dashboardPIDFile = "dashboard.pid"
	dashboardLogFile = "dashboard.log"
	dashboardEnvFile = "dev.env"
)

// runDashboard dispatches `sparkwing dashboard <verb>`.
func runDashboard(args []string) error {
	if handleParentHelp(cmdDashboard, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDashboard, os.Stdout)
		return nil
	}
	switch args[0] {
	case "start":
		return runDashboardStart(args[1:])
	case "kill", "stop":
		return runDashboardKill(args[1:])
	case "status":
		return runDashboardStatus(args[1:])
	default:
		PrintHelp(cmdDashboard, os.Stderr)
		return fmt.Errorf("dashboard: unknown subcommand %q", args[0])
	}
}

type dashboardPaths struct {
	home string
	pid  string
	log  string
}

func resolveDashboardPaths(homeOverride string) (dashboardPaths, error) {
	home := homeOverride
	if home == "" {
		home = os.Getenv("SPARKWING_HOME")
	}
	if home == "" {
		paths, err := orchestrator.DefaultPaths()
		if err != nil {
			return dashboardPaths{}, fmt.Errorf("resolve SPARKWING_HOME: %w", err)
		}
		home = paths.Root
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return dashboardPaths{}, fmt.Errorf("mkdir %s: %w", home, err)
	}
	return dashboardPaths{
		home: home,
		pid:  filepath.Join(home, dashboardPIDFile),
		log:  filepath.Join(home, dashboardLogFile),
	}, nil
}

// readLivePID returns (pid, true) if dashboard.pid points at a running
// process, (0, false) otherwise. A missing or stale PID file is not
// an error -- both mean "not running."
func readLivePID(pidPath string) (int, bool) {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !processAlive(pid) {
		return 0, false
	}
	return pid, true
}

func runDashboardStart(args []string) error {
	fs := flag.NewFlagSet(cmdDashboardStart.Path, flag.ContinueOnError)
	var addr, home, on, logStore, artifactStore string
	var readOnly, noLocalStore bool
	fs.StringVar(&addr, "addr", "127.0.0.1:4343", "bind address for the unified dashboard+api server")
	fs.StringVar(&home, "home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	fs.StringVar(&on, "on", "",
		"profile name from ~/.config/sparkwing/profiles.yaml; uses its log_store + artifact_store fields")
	fs.StringVar(&logStore, "log-store", "",
		"pluggable log backend URL: fs:///abs/path or s3://bucket/prefix. Overrides --on. Intended for ci-embedded VMs without a profiles.yaml")
	fs.StringVar(&artifactStore, "artifact-store", "",
		"pluggable artifact backend URL: fs:///abs/path or s3://bucket/prefix. Overrides --on. Intended for ci-embedded VMs without a profiles.yaml")
	fs.BoolVar(&readOnly, "read-only", false,
		"reject writes on /api/v1/* (auth + webhooks remain open)")
	fs.BoolVar(&noLocalStore, "no-local-store", false,
		"skip the local SQLite store; list runs from --artifact-store instead. Requires --log-store + --artifact-store. Powers tailing CI runs from a fresh laptop without an ingest step.")
	if err := parseAndCheck(cmdDashboardStart, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	// --on resolves to a profile; URL flags override its storage
	// fields when both are set so an operator can spot-check a bucket
	// without editing profiles.yaml.
	if on != "" {
		prof, err := resolveProfile(on)
		if err != nil {
			return err
		}
		if logStore == "" {
			logStore = prof.LogStore
		}
		if artifactStore == "" {
			artifactStore = prof.ArtifactStore
		}
	}

	dp, err := resolveDashboardPaths(home)
	if err != nil {
		return err
	}

	if pid, alive := readLivePID(dp.pid); alive {
		baseURL := readBaseURL(dp.home)
		if baseURL == "" {
			baseURL = "http://" + addr
		}
		fmt.Fprintf(os.Stdout, "dashboard already running (pid %d) at %s\n", pid, baseURL)
		fmt.Fprintln(os.Stdout, "stop with: sparkwing dashboard kill")
		return nil
	}

	logF, err := os.OpenFile(dp.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", dp.log, err)
	}
	defer logF.Close()

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}

	superviseArgs := []string{"__dashboard-supervise",
		"--addr", addr,
		"--home", dp.home,
		"--pid", dp.pid,
	}
	if logStore != "" {
		superviseArgs = append(superviseArgs, "--log-store", logStore)
	}
	if artifactStore != "" {
		superviseArgs = append(superviseArgs, "--artifact-store", artifactStore)
	}
	if readOnly {
		superviseArgs = append(superviseArgs, "--read-only")
	}
	if noLocalStore {
		if logStore == "" || artifactStore == "" {
			return fmt.Errorf("--no-local-store requires --log-store and --artifact-store (or an --on profile that supplies them)")
		}
		superviseArgs = append(superviseArgs, "--no-local-store")
	}
	cmd := exec.Command(self, superviseArgs...)
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Env = os.Environ()
	cmd.SysProcAttr = newDetachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn supervisor: %w", err)
	}
	pid := cmd.Process.Pid
	// Don't Wait -- detached on purpose. The OS reaps on exit; we'll
	// pick up via PID file + kill(0) on subsequent invocations.
	_ = cmd.Process.Release()

	baseURL := "http://" + addr
	if err := waitForListener(addr, 3*time.Second); err != nil {
		fmt.Fprintf(os.Stderr,
			"warn: dashboard didn't accept connections within 3s; check %s\n", dp.log)
	}
	fmt.Fprintln(os.Stdout, bannerLine())
	fmt.Fprintf(os.Stdout, "  dashboard:  %s\n", baseURL)
	fmt.Fprintf(os.Stdout, "  api:        %s/api/v1\n", baseURL)
	fmt.Fprintf(os.Stdout, "  home:       %s\n", dp.home)
	fmt.Fprintf(os.Stdout, "  log:        %s\n", dp.log)
	fmt.Fprintf(os.Stdout, "  pid:        %d\n", pid)
	fmt.Fprintln(os.Stdout, bannerLine())
	fmt.Fprintln(os.Stdout, "stop with: sparkwing dashboard kill")
	return nil
}

func runDashboardKill(args []string) error {
	fs := flag.NewFlagSet(cmdDashboardKill.Path, flag.ContinueOnError)
	var home string
	fs.StringVar(&home, "home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdDashboardKill, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	dp, err := resolveDashboardPaths(home)
	if err != nil {
		return err
	}
	pid, alive := readLivePID(dp.pid)
	if !alive {
		_ = os.Remove(dp.pid)
		fmt.Fprintln(os.Stdout, "dashboard not running")
		return nil
	}
	if err := signalTerminate(pid); err != nil {
		return fmt.Errorf("terminate pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(dp.pid)
			fmt.Fprintf(os.Stdout, "dashboard stopped (pid %d)\n", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Soft shutdown stalled; escalate.
	_ = signalKill(pid)
	_ = os.Remove(dp.pid)
	fmt.Fprintf(os.Stdout, "dashboard force-killed (pid %d, terminate ignored)\n", pid)
	return nil
}

func runDashboardStatus(args []string) error {
	fs := flag.NewFlagSet(cmdDashboardStatus.Path, flag.ContinueOnError)
	var home string
	fs.StringVar(&home, "home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdDashboardStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	dp, err := resolveDashboardPaths(home)
	if err != nil {
		return err
	}
	pid, alive := readLivePID(dp.pid)
	if !alive {
		fmt.Fprintln(os.Stdout, "dashboard not running")
		return exitErrorf(1, "not running")
	}
	baseURL := readBaseURL(dp.home)
	if baseURL == "" {
		baseURL = "(unknown URL; dev.env missing)"
	}
	fmt.Fprintf(os.Stdout, "dashboard running (pid %d) at %s\n", pid, baseURL)
	fmt.Fprintf(os.Stdout, "  home:  %s\n", dp.home)
	fmt.Fprintf(os.Stdout, "  log:   %s\n", dp.log)
	return nil
}

// runDashboardSupervise is the body of the detached child. It writes
// its own PID, runs localws.Run in foreground, and removes the PID
// file on clean exit.
func runDashboardSupervise(args []string) error {
	fs := flag.NewFlagSet("__dashboard-supervise", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:4343", "")
	home := fs.String("home", "", "")
	pidPath := fs.String("pid", "", "")
	logStoreURL := fs.String("log-store", "", "")
	artifactStoreURL := fs.String("artifact-store", "", "")
	readOnly := fs.Bool("read-only", false, "")
	noLocalStore := fs.Bool("no-local-store", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *home == "" || *pidPath == "" {
		return errors.New("__dashboard-supervise: --home and --pid required")
	}

	if err := os.WriteFile(*pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	defer os.Remove(*pidPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := localws.Options{
		Addr:         *addr,
		Home:         *home,
		ReadOnly:     *readOnly,
		NoLocalStore: *noLocalStore,
	}
	if *logStoreURL != "" {
		ls, err := storeurl.OpenLogStore(ctx, *logStoreURL)
		if err != nil {
			return fmt.Errorf("--log-store: %w", err)
		}
		opts.LogStore = ls
		opts.LogStoreLabel = schemeOf(*logStoreURL)
	}
	if *artifactStoreURL != "" {
		as, err := storeurl.OpenArtifactStore(ctx, *artifactStoreURL)
		if err != nil {
			return fmt.Errorf("--artifact-store: %w", err)
		}
		opts.ArtifactStore = as
		opts.ArtifactStoreLabel = schemeOf(*artifactStoreURL)
	}

	if err := localws.Run(ctx, opts); err != nil {
		return fmt.Errorf("local-ws: %w", err)
	}
	return nil
}

// schemeOf extracts the scheme prefix from a store URL ("fs", "s3").
func schemeOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return raw[:i]
	}
	return "custom"
}

// readBaseURL pulls the base URL localws wrote into dev.env. Returns
// empty string when the file is missing or malformed.
func readBaseURL(home string) string {
	b, err := os.ReadFile(filepath.Join(home, dashboardEnvFile))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SPARKWING_CONTROLLER_URL=") {
			return strings.TrimPrefix(line, "SPARKWING_CONTROLLER_URL=")
		}
	}
	return ""
}

// waitForListener polls the bind address until a TCP connect succeeds
// or the deadline expires. Used by `start` to delay printing the URL
// until the supervisor's listener is actually live, so the operator
// can curl it immediately.
func waitForListener(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("listener at %s not ready", addr)
}

func bannerLine() string {
	const n = 60
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = '-'
	}
	return string(buf)
}
