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

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/localws"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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
	var addr, home, logStore, artifactStore string
	var readOnly, noLocalStore bool
	fs.StringVar(&addr, "addr", "127.0.0.1:4343", "bind address for the unified dashboard+api server")
	fs.StringVar(&home, "home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	fs.StringVar(&logStore, "log-store", "",
		"pluggable log backend URL: fs:///abs/path or s3://bucket/prefix")
	fs.StringVar(&artifactStore, "artifact-store", "",
		"pluggable artifact backend URL: fs:///abs/path or s3://bucket/prefix")
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

	// Fail loudly in the foreground if this binary was built without
	// the dashboard bundle; otherwise the detached supervisor would
	// crash into dashboard.log where the user is unlikely to look.
	if err := web.VerifyBundleEmbedded(); err != nil {
		return err
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

	// Pre-check the bind address. If something else is already on it
	// (a stale sparkwing-local-ws, another dashboard supervisor we
	// don't track, a port-clash with a dev server), the supervisor
	// would crash silently into dashboard.log and `kill` would then
	// report "not running" -- because we never got to write the PID
	// file. Surface the clash here instead.
	if holder, err := portHolder(addr); err != nil {
		return err
	} else if holder != "" {
		return fmt.Errorf("address %s already in use by %s; free it or pass --addr to bind elsewhere", addr, holder)
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

	superviseArgs := []string{
		"__dashboard-supervise",
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
			return fmt.Errorf("--no-local-store requires --log-store and --artifact-store (or an --profile profile that supplies them)")
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
		_ = signalTerminate(pid)
		tail := tailFile(dp.log, 40)
		if tail == "" {
			tail = "(empty)"
		}
		return fmt.Errorf("dashboard supervisor (pid %d) failed to accept connections within 3s; tail of %s:\n%s", pid, dp.log, tail)
	}
	// Confirm the supervisor wrote its PID file -- otherwise `kill`
	// later will say "not running" even though the listener is up.
	if _, alive := readLivePID(dp.pid); !alive {
		_ = signalTerminate(pid)
		return fmt.Errorf("dashboard supervisor came up but never wrote %s; check %s", dp.pid, dp.log)
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
	defer func() { _ = os.Remove(*pidPath) }()

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

// portHolder returns a human-readable description of the process bound
// to addr ("<command> pid <pid>"), or empty string if the port is free.
// Returns a non-nil error only on unexpected listener-creation failures
// -- "address in use" is conveyed via the holder string, not an error.
func portHolder(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return "", nil
	}
	// errno EADDRINUSE on POSIX, WSAEADDRINUSE on Windows. We don't
	// match on the errno value (cross-platform pain); the substring
	// "in use" is in both standard messages.
	if !strings.Contains(strings.ToLower(err.Error()), "in use") {
		return "", fmt.Errorf("probe %s: %w", addr, err)
	}
	// Best-effort identification. lsof is on macOS + most Linux
	// distros; fall back to "another process" if it's missing.
	port := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		port = addr[i+1:]
	}
	out, lerr := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fcp").Output()
	if lerr != nil || len(out) == 0 {
		return "another process", nil
	}
	var cmdName, pidStr string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pidStr = line[1:]
		case 'c':
			cmdName = line[1:]
		}
	}
	switch {
	case cmdName != "" && pidStr != "":
		return fmt.Sprintf("%s (pid %s)", cmdName, pidStr), nil
	case pidStr != "":
		return fmt.Sprintf("pid %s", pidStr), nil
	default:
		return "another process", nil
	}
}

// tailFile returns the last n lines of path, or empty string on any
// error. Used to surface supervisor crash output to the foreground.
func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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
