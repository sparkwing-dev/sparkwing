package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/localws"
)

func dashboardHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext}, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func dashboardFlags(command Command, args []string, start bool) (dashboardOptions, string, *flag.FlagSet, error) {
	o := dashboardOptions{Addr: "127.0.0.1:4343"}
	fs := flag.NewFlagSet(command.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	fs.StringVar(&o.Home, "home", "", "state directory")
	if start {
		fs.StringVar(&o.Addr, "addr", o.Addr, "bind address")
		fs.StringVar(&o.LogStore, "log-store", "", "log backend URL")
		fs.StringVar(&o.ArtifactStore, "artifact-store", "", "artifact backend URL")
		fs.StringVar(&o.Profile, "profile", "", "storage profile")
		fs.StringVar(&o.AllowOrigins, "allow-origin", "", "browser origins")
		fs.BoolVar(&o.ReadOnly, "read-only", false, "reject writes")
		fs.BoolVar(&o.NoLocalStore, "no-local-store", false, "skip local store")
		fs.BoolVar(&o.AllowRemote, "allow-remote", false, "allow non-loopback bind")
	}
	if err := parseAndCheck(command, fs, args); err != nil {
		return o, "", fs, err
	}
	if start {
		if _, _, err := net.SplitHostPort(o.Addr); err != nil {
			return o, "", fs, fmt.Errorf("invalid --addr: %w", err)
		}
	}
	mode, err := resolveOutputFormat(*output, command.Path)
	return o, mode, fs, err
}
func runDashboardStart(args []string) error   { return runDashboardLaunch(args, false) }
func runDashboardRestart(args []string) error { return runDashboardLaunch(args, true) }
func runDashboardLaunch(args []string, restart bool) error {
	command := cmdDashboardStart
	action := "start"
	if restart {
		command = cmdDashboardRestart
		action = "restart"
	}
	o, mode, fs, err := dashboardFlags(command, args, true)
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	dp, err := resolveDashboardPaths(o.Home)
	if err != nil {
		return err
	}
	out, record, inspectErr := inspectDashboard(dp, action)
	if out.State == "running" && !restart {
		out.Outcome = "already_running"
		out.Warning = "Dashboard already running; PID and effective options were left unchanged. Use serve restart to replace it."
		fmt.Fprintln(os.Stderr, "warning:", out.Warning)
		if err = renderDashboard(out, mode); err != nil {
			return err
		}
		return nil
	}
	if inspectErr != nil || out.State == "unknown" {
		out.Outcome = "unverified"
		out.Warning = "Cannot verify dashboard ownership; no process was changed."
		if err = renderDashboard(out, mode); err != nil {
			return err
		}
		return exitErrorf(2, "dashboard ownership unverified: %v", inspectErr)
	}
	if restart && out.State == "running" {
		o = mergeDashboardOptions(record.Options, o, fs)
		o.Home = dp.home
	}
	if _, err = net.ResolveTCPAddr("tcp", o.Addr); err != nil {
		return fmt.Errorf("invalid --addr: %w", err)
	}
	for _, store := range []struct {
		flag, value string
		artifact    bool
	}{{"log-store", o.LogStore, false}, {"artifact-store", o.ArtifactStore, true}} {
		if err = validateDashboardStoreURL(store.value, store.artifact); err != nil {
			return fmt.Errorf("--%s: %w", store.flag, err)
		}
	}
	if !o.AllowRemote && !localws.LoopbackBind(o.Addr) {
		return fmt.Errorf("--addr %s is not loopback; pass --allow-remote to accept unauthenticated remote access", o.Addr)
	}
	if err = web.VerifyBundleEmbedded(); err != nil {
		return err
	}
	supplied := false
	if o.Profile != "" {
		p, e := resolveProfileFlag(o.Profile)
		if e != nil {
			return e
		}
		supplied = p.Logs != nil && p.Cache != nil
	}
	if o.NoLocalStore && (o.LogStore == "" || o.ArtifactStore == "") && !supplied {
		return errors.New("--no-local-store requires --log-store and --artifact-store, or a profile supplying both")
	}
	if _, err = dashboardBoot(); err != nil {
		return err
	}
	if err = ensureDashboardHome(dp); err != nil {
		return err
	}
	unlock, err := lockDashboard(dp)
	if err != nil {
		return err
	}
	defer unlock()

	current, currentRecord, currentErr := inspectDashboard(dp, action)
	if currentErr != nil {
		return dashboardFailure(dp, action, mode, currentErr)
	}
	if current.State != out.State || current.PID != out.PID || currentRecord.Instance != record.Instance {
		if !restart && current.State == "running" {
			current.Outcome = "already_running"
			current.Warning = "Dashboard already running; the concurrent start was left unchanged."
			fmt.Fprintln(os.Stderr, "warning:", current.Warning)
			return renderDashboard(current, mode)
		}
		return dashboardFailure(dp, action, mode, errors.New("dashboard changed during validation; retry the explicit action"))
	}
	if restart && out.State == "running" {
		if err = stopOwnedDashboard(record); err != nil {
			return err
		}
		if err = removeDashboardRecord(dp, record); err != nil {
			return err
		}
	}
	if holder, e := portHolder(o.Addr); e != nil {
		return e
	} else if holder != "" {
		return dashboardFailure(dp, action, mode, fmt.Errorf("address %s already in use by %s; no port owner was stopped", o.Addr, holder))
	}
	logFile, err := fssecure.OpenFile(dp.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer logFile.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		return err
	}
	instance := hex.EncodeToString(bytes)
	argv := []string{"__dashboard-supervise", "--home", dp.home, "--pid", dp.pid, "--version", installedVersion(), "--instance", instance}
	argv = append(argv, dashboardOptionArgs(o)...)
	commandProcess := exec.Command(self, argv...)
	commandProcess.Stdout = logFile
	commandProcess.Stderr = logFile
	commandProcess.Env = os.Environ()
	commandProcess.SysProcAttr = newDetachSysProcAttr()
	offset := fileSize(dp.log)
	if err = commandProcess.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	// The starting CLI must reap early failure rather than mistake a zombie for readiness.
	go func() { dashboardCleanupError("wait for dashboard", commandProcess.Wait()); close(exited) }()
	result, err := waitDashboard(dp, commandProcess.Process.Pid, exited)
	if err != nil {
		// A retained child handle targets this spawn even if its numeric PID is reused.
		dashboardCleanupError("stop failed dashboard", commandProcess.Process.Kill())
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
		return dashboardFailure(dp, action, mode, fmt.Errorf("%w; startup log:\n%s", err, tailFileFrom(dp.log, offset, 40)))
	}
	result.Action = action
	result.Outcome = "started"
	if restart {
		result.Outcome = "restarted"
	}
	return renderDashboard(result, mode)
}

func validateDashboardStoreURL(raw string, artifact bool) error {
	if raw == "" {
		return nil
	}
	// Opening a backend may create directories or load credentials, so preflight checks syntax only.
	scheme, rest, found := strings.Cut(raw, "://")
	if !found {
		return errors.New("storage URL requires scheme://")
	}
	switch scheme {
	case "fs":
		if strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "~") {
			return nil
		}
		return errors.New("filesystem storage requires an absolute path")
	case "s3":
		u, err := neturl.Parse(raw)
		if err == nil && u.Host != "" {
			return nil
		}
		return errors.New("S3 storage requires a valid bucket URL")
	case "http", "https":
		if artifact {
			u, err := neturl.Parse(raw)
			if err == nil && u.Host != "" {
				return nil
			}
		}
	}
	return errors.New("unsupported storage URL; use fs or s3 for logs, or fs, s3, http or https for artifacts")
}

func dashboardOptionArgs(o dashboardOptions) []string {
	a := []string{"--addr", o.Addr}
	for _, v := range [][2]string{{"--log-store", o.LogStore}, {"--artifact-store", o.ArtifactStore}, {"--profile", o.Profile}, {"--allow-origin", o.AllowOrigins}} {
		if v[1] != "" {
			a = append(a, v[0], v[1])
		}
	}
	for _, v := range []struct {
		name  string
		value bool
	}{{"--read-only", o.ReadOnly}, {"--no-local-store", o.NoLocalStore}, {"--allow-remote", o.AllowRemote}} {
		if v.value {
			a = append(a, v.name)
		}
	}
	return a
}

func mergeDashboardOptions(old, next dashboardOptions, fs *flag.FlagSet) dashboardOptions {
	if fs.Changed("addr") {
		old.Addr = next.Addr
	}
	if fs.Changed("log-store") {
		old.LogStore = next.LogStore
	}
	if fs.Changed("artifact-store") {
		old.ArtifactStore = next.ArtifactStore
	}
	if fs.Changed("profile") {
		old.Profile = next.Profile
	}
	if fs.Changed("allow-origin") {
		old.AllowOrigins = next.AllowOrigins
	}
	if fs.Changed("read-only") {
		old.ReadOnly = next.ReadOnly
	}
	if fs.Changed("no-local-store") {
		old.NoLocalStore = next.NoLocalStore
	}
	if fs.Changed("allow-remote") {
		old.AllowRemote = next.AllowRemote
	}
	return old
}

func runDashboardStop(args []string) error {
	o, mode, _, err := dashboardFlags(cmdDashboardStop, args, false)
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	dp, err := resolveDashboardPaths(o.Home)
	if err != nil {
		return err
	}
	if _, err = os.Stat(dp.home); errors.Is(err, os.ErrNotExist) {
		out, _, inspectErr := inspectDashboard(dp, "stop")
		if inspectErr != nil {
			return inspectErr
		}
		out.Outcome = "already_stopped"
		return renderDashboard(out, mode)
	}
	unlock, err := lockDashboard(dp)
	if err != nil {
		return err
	}
	defer unlock()
	out, record, err := inspectDashboard(dp, "stop")
	if err != nil || out.State == "unknown" || out.State == "running" && out.Ownership != "owned" {
		out.Outcome = "unverified"
		if e := renderDashboard(out, mode); e != nil {
			return e
		}
		return exitErrorf(2, "dashboard ownership unverified; no process was signaled")
	}
	out.Outcome = "already_stopped"
	if out.State == "running" {
		if err = stopOwnedDashboard(record); err != nil {
			return err
		}
		if err = removeDashboardRecord(dp, record); err != nil {
			return err
		}
		out.Outcome = "stopped"
	}
	out.State = "stopped"
	out.Readiness = "not_ready"
	return renderDashboard(out, mode)
}

func runDashboardStatus(args []string) error {
	o, mode, _, err := dashboardFlags(cmdDashboardStatus, args, false)
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	dp, err := resolveDashboardPaths(o.Home)
	if err != nil {
		return err
	}
	out, _, inspectErr := inspectDashboard(dp, "status")
	if err = renderDashboard(out, mode); err != nil {
		return err
	}
	if inspectErr != nil || out.State == "unknown" || out.State == "running" && (out.Ownership != "owned" || out.Readiness != "ready") {
		return exitErrorf(2, "dashboard ownership or readiness is unverified")
	}
	if out.State == "stopped" {
		return exitErrorf(1, "not running")
	}
	return nil
}

func removeDashboardRecord(dp dashboardPaths, expected dashboardRecord) error {
	actual, err := readDashboardRecord(dp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if actual.PID != expected.PID || actual.Instance != expected.Instance {
		return errors.New("dashboard ownership changed during cleanup")
	}
	for _, path := range []string{dashboardStatePath(dp), dp.pid} {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func lockDashboard(dp dashboardPaths) (func(), error) {
	f, err := fssecure.OpenFile(filepath.Join(dp.home, "dashboard.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err = dashboardLock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("dashboard lifecycle is busy: %w", err)
	}
	return func() { _ = f.Close() }, nil
}

func dashboardFailure(dp dashboardPaths, action, mode string, cause error) error {
	out, _, inspectErr := inspectDashboard(dp, action)
	if inspectErr != nil {
		out.Warning = "Service state could not be verified after failure."
	}
	out.Outcome = "failed"
	if err := renderDashboard(out, mode); err != nil {
		return err
	}
	return exitErrorf(2, "%v", cause)
}

func dashboardCleanupError(operation string, err error) {
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrProcessDone) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	}
}

// ensureDashboardHome creates the state directory the service writes into.
// Resolving paths deliberately touches no filesystem, so help and a refused
// argument leave a fresh home absent; the first writer is what makes it
// private.
func ensureDashboardHome(dp dashboardPaths) error {
	return fssecure.EnsureDir(dp.home)
}
