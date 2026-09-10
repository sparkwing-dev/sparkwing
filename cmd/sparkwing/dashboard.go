package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/localws"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

const (
	dashboardPIDFile = "dashboard.pid"
	dashboardLogFile = "dashboard.log"
	dashboardEnvFile = "dev.env"
)

const dashboardStartTimeout = 30 * time.Second

func removedDashboardCommand(args []string) bool {
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "-o" || arg == "--output":
			if len(args) < 2 {
				return false
			}
			args = args[2:]
		case arg == "help" || arg == "--help" || arg == "-h" || strings.HasPrefix(arg, "--output=") || strings.HasPrefix(arg, "-o=") || (strings.HasPrefix(arg, "-o") && len(arg) > 2):
			args = args[1:]
		default:
			return arg == "dashboard"
		}
	}
	return false
}

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
	case "stop":
		return runDashboardStop(args[1:])
	case "restart":
		return runDashboardRestart(args[1:])
	case "logs":
		return runDashboardLogs(args[1:])
	case "status":
		return runDashboardStatus(args[1:])
	default:
		PrintHelp(cmdDashboard, os.Stderr)
		return fmt.Errorf("serve: unknown subcommand %q", args[0])
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
		paths, err := orchestrator.DefaultPaths()
		if err != nil {
			return dashboardPaths{}, fmt.Errorf("resolve SPARKWING_HOME: %w", err)
		}
		home = paths.Root
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return dashboardPaths{}, err
	}
	home = abs
	return dashboardPaths{
		home: home,
		pid:  filepath.Join(home, dashboardPIDFile),
		log:  filepath.Join(home, dashboardLogFile),
	}, nil
}

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

func runDashboardSupervise(args []string) error {
	fs := flag.NewFlagSet("__dashboard-supervise", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:4343", "")
	home := fs.String("home", "", "")
	pidPath := fs.String("pid", "", "")
	logStoreURL := fs.String("log-store", "", "")
	artifactStoreURL := fs.String("artifact-store", "", "")
	readOnly := fs.Bool("read-only", false, "")
	allowRemote := fs.Bool("allow-remote", false, "")
	allowOrigins := fs.String("allow-origin", "", "")
	noLocalStore := fs.Bool("no-local-store", false, "")
	profileName := fs.String("profile", "", "")
	version := fs.String("version", "", "")
	instance := fs.String("instance", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *home == "" || *pidPath == "" {
		return errors.New("__dashboard-supervise: --home and --pid required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := localws.Options{
		Addr:         *addr,
		Home:         *home,
		ReadOnly:     *readOnly,
		AllowRemote:  *allowRemote,
		AllowOrigins: splitCSV(*allowOrigins),
		NoLocalStore: *noLocalStore,
		Version:      *version,
		Instance:     *instance,
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
	if err := applyDashboardProfile(ctx, &opts, *profileName); err != nil {
		return err
	}

	if !opts.AllowRemote && !localws.LoopbackBind(opts.Addr) {
		return errors.New("non-loopback dashboard requires --allow-remote")
	}
	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	defer func() { dashboardCleanupError("close dashboard listener", listener.Close()) }()
	opts.Listener = listener
	opts.Addr = listener.Addr().String()
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		return err
	}
	boot, err := dashboardBoot()
	if err != nil {
		return err
	}
	dp, err := resolveDashboardPaths(*home)
	if err != nil {
		return err
	}
	record := dashboardRecord{PID: os.Getpid(), Birth: birth, Boot: boot, Instance: *instance, Artifact: dashboardRunningArtifact(), Options: dashboardOptions{Addr: opts.Addr, LogStore: *logStoreURL, ArtifactStore: *artifactStoreURL, Profile: *profileName, AllowOrigins: *allowOrigins, ReadOnly: *readOnly, NoLocalStore: *noLocalStore, AllowRemote: *allowRemote}}
	if record.Instance == "" {
		record.Instance = fmt.Sprintf("%d-%s", record.PID, record.Birth)
		opts.Instance = record.Instance
	}
	if err = writeDashboardRecord(dp, record); err != nil {
		return err
	}
	if err = fssecure.WriteFile(*pidPath, []byte(strconv.Itoa(os.Getpid())+"\n")); err != nil {
		return err
	}
	defer func() { dashboardCleanupError("remove dashboard state", removeDashboardRecord(dp, record)) }()
	if err := localws.Run(ctx, opts); err != nil {
		return fmt.Errorf("local-ws: %w", err)
	}
	return nil
}

func schemeOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return raw[:i]
	}
	return "custom"
}

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

func portHolder(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return "", nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "in use") {
		return "", fmt.Errorf("probe %s: %w", addr, err)
	}
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

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func tailFileFrom(path string, byteOffset int64, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if byteOffset > 0 && byteOffset <= int64(len(b)) {
		b = b[byteOffset:]
	}
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func probeDashboardVersion(home, addr string) (localws.VersionInfo, bool) {
	candidates := []string{}
	if base := readBaseURL(home); base != "" {
		candidates = append(candidates, base)
	}
	candidates = append(candidates, "http://"+addr)

	client := &http.Client{Timeout: 2 * time.Second}
	seen := map[string]bool{}
	for _, base := range candidates {
		base = strings.TrimRight(base, "/")
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		if info, ok := getDashboardVersion(client, base); ok {
			return info, true
		}
	}
	return localws.VersionInfo{}, false
}

func getDashboardVersion(client *http.Client, base string) (localws.VersionInfo, bool) {
	resp, err := client.Get(base + "/api/v1/version")
	if err != nil {
		return localws.VersionInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return localws.VersionInfo{}, false
	}
	var info localws.VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Version == "" {
		return localws.VersionInfo{}, false
	}
	return info, true
}

func dashboardIsNewer(running, mine string) bool {
	if !semver.IsValid(running) || !semver.IsValid(mine) {
		return false
	}
	return semver.Compare(running, mine) > 0
}

func waitForListenerOrExit(addr string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("exited during startup before accepting connections")
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("failed to accept connections within %s", timeout)
}

func bannerLine() string {
	const n = 60
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = '-'
	}
	return string(buf)
}

func applyDashboardProfile(ctx context.Context, opts *localws.Options, profileName string) error {
	if profileName == "" {
		return nil
	}
	p, err := resolveProfileFlag(profileName)
	if err != nil {
		return err
	}
	if opts.LogStore == nil && p.Logs != nil {
		ls, err := storeurl.OpenLogStoreFromSpec(ctx, *p.Logs, nil)
		if err != nil {
			return fmt.Errorf("--profile %s logs surface: %w", profileName, err)
		}
		opts.LogStore = ls
		opts.LogStoreLabel = p.Logs.Type
	}
	if opts.ArtifactStore == nil && p.Cache != nil {
		as, err := storeurl.OpenArtifactStoreFromSpec(ctx, *p.Cache, nil)
		if err != nil {
			return fmt.Errorf("--profile %s cache surface: %w", profileName, err)
		}
		opts.ArtifactStore = as
		opts.ArtifactStoreLabel = p.Cache.Type
	}
	return nil
}

func stopSupervisor(pid int, pidPath string) error {
	if err := signalTerminate(pid); err != nil {
		return fmt.Errorf("terminate pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			dashboardCleanupError("remove consumer PID", os.Remove(pidPath))
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	dashboardCleanupError("stop consumer", signalKill(pid))
	dashboardCleanupError("remove consumer PID", os.Remove(pidPath))
	return nil
}
