package wingd

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func runnerSupervisorHost(t *testing.T) crontimer.Host {
	t.Helper()
	root := t.TempDir()
	return crontimer.Host{
		GOOS: "linux", Home: root, SparkwingHome: filepath.Join(root, "state $cash%cash & quotes\""),
		Binary: filepath.Join(root, "sparkwing $cash%cash & quotes\""), PathEnv: "/bin:/custom path",
		Env: map[string]string{"CAPTURED": "value $cash%cash & quotes\"", "HOME": "/wrong"}, UID: 501,
	}
}

func TestLocalRunnerServiceNamesSeparateHomesAndInstances(t *testing.T) {
	a := LocalRunnerServiceName("/home/state", "instance")
	if a != LocalRunnerServiceName("/home/state/.", "instance") ||
		a == LocalRunnerServiceName("/home/other", "instance") ||
		a == LocalRunnerServiceName("/home/state", "other") {
		t.Fatal("runner service identity did not distinguish home and instance")
	}
	for _, ch := range LocalRunnerServiceName("/home/state", "unsafe / instance %") {
		if ch != '-' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			t.Fatalf("unsafe service name character %q", ch)
		}
	}
}

func TestLocalRunnerSystemdManagerFailureNeverDetaches(t *testing.T) {
	for _, failing := range []string{"systemctl", "systemd-run"} {
		t.Run(failing, func(t *testing.T) {
			host := runnerSupervisorHost(t)
			var calls []string
			host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
				calls = append(calls, name)
				if name == "systemctl" && !slices.Equal(args, []string{"--user", "show-environment"}) {
					t.Fatalf("manager probe = %q", args)
				}
				if name == failing {
					return "manager unavailable", errors.New("failed manager")
				}
				return "", nil
			}
			err := startLocalRunnerLinux(t.Context(), host, "instance", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "failed manager") {
				t.Fatalf("startup = %v; want manager failure", err)
			}
			want := []string{"systemctl"}
			if failing == "systemd-run" {
				want = append(want, "systemd-run")
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("commands = %q; want %q", calls, want)
			}
			log := filepath.Join(host.SparkwingHome, "runners", LocalRunnerServiceName(host.SparkwingHome, "instance"), "runner.log")
			if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("manager failure attempted unmanaged launch: %v", err)
			}
		})
	}
}

func TestLocalRunnerSystemdKeepsLiteralArgvAndIndependentLifetime(t *testing.T) {
	host := runnerSupervisorHost(t)
	var launch []string
	host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
		if name == "systemd-run" {
			launch = slices.Clone(args)
		}
		return "", nil
	}
	if err := startLocalRunnerLinux(t.Context(), host, "instance", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"--user", "--service-type=exec", "--property=Restart=no", "--property=TimeoutStopSec=infinity", "--property=KillMode=mixed"} {
		if !slices.Contains(launch, arg) {
			t.Fatalf("missing %q in %q", arg, launch)
		}
	}
	dir := filepath.Join(host.SparkwingHome, "runners", LocalRunnerServiceName(host.SparkwingHome, "instance"))
	for _, property := range []string{"EnvironmentFile=" + filepath.Join(dir, "runner.env"), "StandardOutput=append:" + filepath.Join(dir, "runner.log")} {
		if !slices.Contains(launch, "--property="+property) {
			t.Fatalf("native property bytes changed: %q in %q", property, launch)
		}
	}
	for _, arg := range launch {
		if strings.Contains(arg, "BindsTo") || strings.Contains(arg, "PartOf") || strings.Contains(arg, "controller") || arg == "--scope" || arg == "--wait" || arg == "--pipe" {
			t.Fatalf("runner depends on invoking controller: %q", launch)
		}
	}
	index := slices.Index(launch, "--")
	want := []string{host.Binary}
	for _, arg := range localRunnerArguments(host, "instance")[1:] {
		want = append(want, strings.ReplaceAll(arg, "$", "$$"))
	}
	if index < 0 || !slices.Equal(launch[index+1:], want) {
		t.Fatalf("runner argv = %q; want %q", launch, want)
	}
	for _, arg := range launch {
		if strings.Contains(arg, host.Env["CAPTURED"]) {
			t.Fatal("captured environment exposed through manager argv")
		}
	}
}

func TestLocalRunnerSystemdInspectionFailureDoesNotLaunch(t *testing.T) {
	host := runnerSupervisorHost(t)
	host.Exec = func(context.Context, string, ...string) (string, error) {
		t.Fatal("manager contacted after failed boot inspection")
		return "", nil
	}
	marker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startLocalRunnerLinux(t.Context(), host, "instance", marker); err == nil {
		t.Fatal("non-directory boot marker accepted")
	}
	if err := startLocalRunnerLinux(t.Context(), host, "instance", filepath.Join(marker, "child")); err == nil {
		t.Fatal("failed boot inspection accepted")
	}
}

func TestLocalRunnerSystemdEnvironmentFileKeepsQuotedValuesPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX environment-file quoting")
	}
	host := runnerSupervisorHost(t)
	host.Env["CAPTURED"] = " leading space\ncarriage\r tab\t slash\\ quotes\"' dollar$ percent% backtick` trailing "
	host.Exec = func(context.Context, string, ...string) (string, error) { return "", nil }
	if err := startLocalRunnerLinux(t.Context(), host, "instance", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.SparkwingHome, "runners", LocalRunnerServiceName(host.SparkwingHome, "instance"), "runner.env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("environment file permissions = %o", got)
	}
	command := procenv.ControlCommand(nil, "/bin/sh", "-c", `. "$1"; printf '%s' "$CAPTURED"`, "read-environment", path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read environment file: %v: %s", err, output)
	}
	if string(output) != host.Env["CAPTURED"] {
		t.Fatalf("captured environment changed: %q", output)
	}
}

func TestLocalRunnerLaunchdCapturesArgvAndDoesNotResurrectInstance(t *testing.T) {
	host := runnerSupervisorHost(t)
	host.GOOS = "darwin"
	var launch []string
	host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
		launch = append([]string{name}, args...)
		return "", nil
	}
	if err := StartLocalRunnerProcess(t.Context(), host, "instance"); err != nil {
		t.Fatal(err)
	}
	if len(launch) != 4 || !slices.Equal(launch[:3], []string{"launchctl", "bootstrap", "gui/501"}) {
		t.Fatalf("launchd invocation = %q", launch)
	}
	body, err := os.ReadFile(launch[3])
	if err != nil {
		t.Fatal(err)
	}
	if got := unitfile.Arguments("darwin", string(body)); !slices.Equal(got, localRunnerArguments(host, "instance")) {
		t.Fatalf("plist argv = %q", got)
	}
	var document any
	if err := xml.Unmarshal(body, &document); err != nil {
		t.Fatalf("invalid launch agent XML: %v", err)
	}
	for _, fragment := range []string{
		"<key>KeepAlive</key><false/>", "<key>ExitTimeOut</key><integer>0</integer>",
		"<key>HOME</key><string>" + unitfile.XMLText(host.Home),
		"<key>CAPTURED</key><string>value $cash%cash &amp; quotes&#34;",
	} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("missing launchd contract %q in %s", fragment, body)
		}
	}
}

func TestLocalRunnerWindowsRefusesCrossAccountSupervision(t *testing.T) {
	host := runnerSupervisorHost(t)
	host.GOOS = "windows"
	host.Exec = func(context.Context, string, ...string) (string, error) {
		t.Fatal("SCM must not launch a runner under a different account")
		return "", nil
	}
	if err := StartLocalRunnerProcess(t.Context(), host, "instance"); err == nil || !strings.Contains(err.Error(), "same-account") {
		t.Fatalf("Windows startup = %v", err)
	}
}

func TestLocalRunnerStartupEnvironmentRetiresWithoutRemovingLogs(t *testing.T) {
	host := runnerSupervisorHost(t)
	dir, err := localRunnerDirectory(host, "instance")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runner.env", "runner.plist", "runner.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveLocalRunnerStartupEnvironment(host.SparkwingHome, "instance"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runner.env", "runner.plist"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("startup input retained: %s %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "runner.log")); err != nil || string(data) != "private" {
		t.Fatalf("cleanup removed log: %q %v", data, err)
	}
}
