package runnersvc

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeExec struct {
	calls []string
	fail  map[string]string
}

func (f *fakeExec) run(name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	for prefix, out := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return out, errors.New("exit status 1")
		}
	}
	return "", nil
}

func linuxHost(t *testing.T, exec *fakeExec) Host {
	t.Helper()
	root := t.TempDir()
	return Host{
		GOOS:       "linux",
		Home:       root,
		ConfigHome: filepath.Join(root, ".config"),
		Binary:     "/usr/local/bin/sparkwing-runner",
		ConfigPath: filepath.Join(root, ".config", "sparkwing", "agent.yaml"),
		Exec:       exec.run,
	}
}

func darwinHost(t *testing.T, exec *fakeExec) Host {
	t.Helper()
	root := t.TempDir()
	return Host{
		GOOS:       "darwin",
		Home:       root,
		ConfigHome: filepath.Join(root, ".config"),
		Binary:     "/usr/local/bin/sparkwing-runner",
		ConfigPath: filepath.Join(root, ".config", "sparkwing", "agent.yaml"),
		LogPath:    filepath.Join(root, ".sparkwing", "runner.log"),
		UID:        501,
		Exec:       exec.run,
	}
}

func TestInstallLinuxWritesUnitAndStartsIt(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	state, err := Install(h)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !state.Installed || !state.Running {
		t.Fatalf("state = %+v", state)
	}
	if state.Path != UnitPath(h) {
		t.Fatalf("path = %q, want %q", state.Path, UnitPath(h))
	}
	body, err := os.ReadFile(state.Path)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	unit := string(body)
	for _, want := range []string{
		Marker,
		"ExecStart=/usr/local/bin/sparkwing-runner agent --config " + h.ConfigPath,
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	info, err := os.Stat(state.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != fileMode {
		t.Errorf("unit mode = %04o, want %04o", info.Mode().Perm(), fileMode)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now sparkwing-runner.service",
	}
	if strings.Join(exec.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", exec.calls, want)
	}
}

func TestInstallDarwinWritesPlistAndLoadsIt(t *testing.T) {
	exec := &fakeExec{}
	h := darwinHost(t, exec)
	state, err := Install(h)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !state.Installed || !state.Running {
		t.Fatalf("state = %+v", state)
	}
	body, err := os.ReadFile(PlistPath(h))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	var parsed any
	if err := xml.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("plist is not well-formed XML: %v", err)
	}
	plist := string(body)
	for _, want := range []string{Marker, "<string>" + Label + "</string>", "<string>agent</string>", h.ConfigPath} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
	if _, err := os.Stat(filepath.Dir(h.LogPath)); err != nil {
		t.Errorf("log directory was not created: %v", err)
	}
	want := []string{
		"launchctl bootout gui/501/com.sparkwing.runner",
		"launchctl bootstrap gui/501 " + PlistPath(h),
	}
	if strings.Join(exec.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", exec.calls, want)
	}
}

func TestInstallRefusesAForeignUnit(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	path := UnitPath(h)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/usr/bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := Install(h)
	if err == nil {
		t.Fatal("Install overwrote a unit sparkwing did not write")
	}
	if !state.Foreign {
		t.Errorf("state = %+v, want Foreign", state)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "/usr/bin/true") {
		t.Errorf("the foreign unit was rewritten: %s", body)
	}
	if len(exec.calls) != 0 {
		t.Errorf("a refused install still talked to systemd: %v", exec.calls)
	}
}

func TestInstallAdoptsAUnitTheShellInstallerWrote(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	path := UnitPath(h)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# sparkwing-runner systemd user service template.\n# " + installerMarker + " -- do not edit directly.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := Install(h)
	if err != nil {
		t.Fatalf("Install over a shell-installed unit: %v", err)
	}
	if state.Foreign {
		t.Errorf("state = %+v, want the shell installer's unit adopted", state)
	}
}

func TestUninstallStopsAndRemoves(t *testing.T) {
	for _, tc := range []struct {
		name  string
		host  func(*testing.T, *fakeExec) Host
		path  func(Host) string
		calls []string
	}{
		{"linux", linuxHost, UnitPath, []string{
			"systemctl --user disable --now sparkwing-runner.service",
			"systemctl --user daemon-reload",
		}},
		{"darwin", darwinHost, PlistPath, []string{
			"launchctl bootout gui/501/com.sparkwing.runner",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install := &fakeExec{}
			h := tc.host(t, install)
			if _, err := Install(h); err != nil {
				t.Fatalf("Install: %v", err)
			}
			exec := &fakeExec{}
			h.Exec = exec.run
			state, err := Uninstall(h)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if state.Installed {
				t.Errorf("state = %+v", state)
			}
			if _, err := os.Stat(tc.path(h)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stat after uninstall = %v, want not-exist", err)
			}
			if strings.Join(exec.calls, "|") != strings.Join(tc.calls, "|") {
				t.Errorf("calls = %v, want %v", exec.calls, tc.calls)
			}
		})
	}
}

func TestUninstallWithNothingInstalledIsQuiet(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	state, err := Uninstall(h)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if state.Installed || state.Detail == "" {
		t.Errorf("state = %+v", state)
	}
	if len(exec.calls) != 0 {
		t.Errorf("a no-op uninstall talked to systemd: %v", exec.calls)
	}
}

func TestUninstallToleratesAServiceAlreadyGone(t *testing.T) {
	install := &fakeExec{}
	h := linuxHost(t, install)
	if _, err := Install(h); err != nil {
		t.Fatalf("Install: %v", err)
	}
	exec := &fakeExec{fail: map[string]string{
		"systemctl --user disable": "Failed to disable unit: Unit file sparkwing-runner.service does not exist.",
	}}
	h.Exec = exec.run
	if _, err := Uninstall(h); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(UnitPath(h)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat after uninstall = %v, want not-exist", err)
	}
}

func TestUnsupportedGOOS(t *testing.T) {
	if _, err := Install(Host{GOOS: "windows"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Install on windows = %v, want ErrUnsupported", err)
	}
	if _, err := Uninstall(Host{GOOS: "windows"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Uninstall on windows = %v, want ErrUnsupported", err)
	}
}

func TestInstallRequiresAbsolutePaths(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	h.Binary = "sparkwing-runner"
	if _, err := Install(h); err == nil {
		t.Fatal("Install accepted a relative binary path")
	}
	h = linuxHost(t, exec)
	h.ConfigPath = "agent.yaml"
	if _, err := Install(h); err == nil {
		t.Fatal("Install accepted a relative config path")
	}
}

func TestSystemdWordQuotesSpacesAndDoublesSpecifiers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/usr/bin/sparkwing-runner", "/usr/bin/sparkwing-runner"},
		{"/opt/my runner/sparkwing-runner", `"/opt/my runner/sparkwing-runner"`},
		{"/opt/100%/runner", "/opt/100%%/runner"},
		{"", `""`},
	} {
		if got := systemdWord(tc.in); got != tc.want {
			t.Errorf("systemdWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
