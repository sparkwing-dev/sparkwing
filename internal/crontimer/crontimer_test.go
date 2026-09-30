package crontimer

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// hack: a prefix match lets a test break exactly the systemctl or launchctl call it cares about.
type rule struct {
	prefix string
	out    string
	err    error
}

type fakeExec struct {
	calls []string
	rules []rule
}

func (f *fakeExec) run(name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	for _, r := range f.rules {
		if strings.HasPrefix(cmd, r.prefix) {
			return r.out, r.err
		}
	}
	return "", nil
}

func (f *fakeExec) fail(prefix, out string) {
	f.rules = append(f.rules, rule{prefix, out, errors.New("exit status 1")})
}

func linuxHost(t *testing.T, e *fakeExec) Host {
	t.Helper()
	root := t.TempDir()
	return Host{
		GOOS:       "linux",
		Home:       root,
		ConfigHome: filepath.Join(root, ".config"),
		Binary:     "/usr/local/bin/sparkwing",
		PathEnv:    "/usr/local/bin:/usr/bin:/bin",
		LogPath:    filepath.Join(root, ".sparkwing", "crons.log"),
		UID:        1000,
		Exec:       e.run,

		SparkwingHome: filepath.Join(root, ".sparkwing"),
	}
}

func darwinHost(t *testing.T, e *fakeExec) Host {
	t.Helper()
	root := t.TempDir()
	return Host{
		GOOS:       "darwin",
		Home:       root,
		ConfigHome: filepath.Join(root, ".config"),
		Binary:     "/opt/homebrew/bin/sparkwing",
		PathEnv:    "/opt/homebrew/bin:/usr/bin:/bin",
		LogPath:    filepath.Join(root, ".sparkwing", "crons.log"),
		UID:        501,
		Exec:       e.run,

		SparkwingHome: filepath.Join(root, ".sparkwing"),
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestInstallLinuxWritesBothUnitsAndEnablesTheTimer(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)

	state, err := Install(h)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	servicePath := filepath.Join(UnitDir(h), h.serviceName())
	timerPath := filepath.Join(UnitDir(h), h.timerName())
	if state.Path != timerPath {
		t.Errorf("State.Path = %q, want the timer at %q", state.Path, timerPath)
	}
	if !state.Installed || !state.Enabled || state.Foreign || state.Stale {
		t.Errorf("State = %+v, want installed and enabled only", state)
	}
	if state.Binary != h.Binary {
		t.Errorf("State.Binary = %q, want %q", state.Binary, h.Binary)
	}

	service := read(t, servicePath)
	for _, want := range []string{
		"# " + Marker,
		"Type=oneshot",
		"KillMode=process",
		"TimeoutStartSec=5m",
		"ExecStart=/usr/local/bin/sparkwing crons tick",
		"Environment=PATH=/usr/local/bin:/usr/bin:/bin",
		"Environment=SPARKWING_HOME=" + filepath.Join(h.Home, ".sparkwing"),
		"StandardOutput=append:" + h.LogPath,
		"StandardError=append:" + h.LogPath,
	} {
		if !strings.Contains(service, want) {
			t.Errorf("service unit missing %q:\n%s", want, service)
		}
	}

	timer := read(t, timerPath)
	for _, want := range []string{
		"# " + Marker,
		"OnCalendar=*-*-* *:*:00",
		"AccuracySec=1s",
		"Persistent=true",
		"Unit=" + h.serviceName(),
		"WantedBy=timers.target",
	} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer unit missing %q:\n%s", want, timer)
		}
	}

	wantCalls := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now sparkwing-crons.timer",
	}
	if !slices.Equal(exec.calls, wantCalls) {
		t.Errorf("exec calls = %v, want %v", exec.calls, wantCalls)
	}
	for _, p := range []string{servicePath, timerPath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("%s mode = %#o, want 0644", p, got)
		}
	}
	if _, err := os.Stat(filepath.Dir(h.LogPath)); err != nil {
		t.Errorf("log directory not created: %v", err)
	}
}

func TestStatusLinuxAfterInstall(t *testing.T) {
	tests := []struct {
		name        string
		rules       []func(*fakeExec)
		binary      string
		wantEnabled bool
		wantStale   bool
		wantDetail  string
	}{
		{
			name:        "enabled and current",
			binary:      "/usr/local/bin/sparkwing",
			wantEnabled: true,
		},
		{
			name:      "enabled but a different binary is asked about",
			binary:    "/home/me/go/bin/sparkwing",
			wantStale: true, wantEnabled: true,
		},
		{
			name:       "no user session",
			rules:      []func(*fakeExec){func(e *fakeExec) { e.fail("systemctl --user is-", "Failed to connect to bus") }},
			binary:     "/usr/local/bin/sparkwing",
			wantDetail: "Failed to connect to bus",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			installer := &fakeExec{}
			h := linuxHost(t, installer)
			if _, err := Install(h); err != nil {
				t.Fatalf("Install: %v", err)
			}

			prober := &fakeExec{}
			for _, apply := range tc.rules {
				apply(prober)
			}
			h.Exec = prober.run
			h.Binary = tc.binary

			state, err := Status(h)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if !state.Installed {
				t.Fatalf("State.Installed = false, want true: %+v", state)
			}
			if state.Binary != "/usr/local/bin/sparkwing" {
				t.Errorf("State.Binary = %q, want the installed binary", state.Binary)
			}
			if state.Enabled != tc.wantEnabled {
				t.Errorf("State.Enabled = %v, want %v (%s)", state.Enabled, tc.wantEnabled, state.Detail)
			}
			if state.Stale != tc.wantStale {
				t.Errorf("State.Stale = %v, want %v", state.Stale, tc.wantStale)
			}
			if tc.wantDetail != "" && !strings.Contains(state.Detail, tc.wantDetail) {
				t.Errorf("State.Detail = %q, want it to carry %q", state.Detail, tc.wantDetail)
			}
			wantProbe := []string{
				"systemctl --user show -p FragmentPath --value sparkwing-crons.timer",
				"systemctl --user is-enabled sparkwing-crons.timer",
				"systemctl --user is-active sparkwing-crons.timer",
			}
			if !slices.Equal(prober.calls, wantProbe) {
				t.Errorf("status calls = %v, want %v", prober.calls, wantProbe)
			}
		})
	}
}

func TestStatusOnAnEmptyHostReportsNothingInstalled(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			exec := &fakeExec{}
			h := linuxHost(t, exec)
			if goos == "darwin" {
				h = darwinHost(t, exec)
			}
			state, err := Status(h)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if state.Installed || state.Enabled || state.Foreign {
				t.Errorf("State = %+v, want an empty host", state)
			}
			if len(exec.calls) != 0 {
				t.Errorf("Status asked the service manager about nothing: %v", exec.calls)
			}
		})
	}
}

func TestInstallDarwinWritesThePlistAndBootstrapsIt(t *testing.T) {
	exec := &fakeExec{}
	h := darwinHost(t, exec)

	state, err := Install(h)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	path := filepath.Join(AgentDir(h), h.plistName())
	if state.Path != path {
		t.Errorf("State.Path = %q, want %q", state.Path, path)
	}
	if !state.Installed || !state.Enabled {
		t.Errorf("State = %+v, want installed and loaded", state)
	}

	body := read(t, path)
	if !strings.HasPrefix(body, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- "+Marker+" -->\n") {
		t.Errorf("plist does not open with the declaration then the marker comment:\n%s", body)
	}
	for _, want := range []string{
		"<key>Label</key>\n  <string>dev.sparkwing.crons</string>",
		"<string>/opt/homebrew/bin/sparkwing</string>",
		"<string>crons</string>",
		"<string>tick</string>",
		"<key>StartInterval</key>\n  <integer>60</integer>",
		"<key>RunAtLoad</key>\n  <false/>",
		"<key>AbandonProcessGroup</key>\n  <true/>",
		"<key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/bin:/bin</string>",
		"<key>SPARKWING_HOME</key>",
		"<key>StandardOutPath</key>\n  <string>" + h.LogPath + "</string>",
		"<key>StandardErrorPath</key>\n  <string>" + h.LogPath + "</string>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "ProcessType") {
		t.Errorf("the agent still asks launchd to throttle it:\n%s", body)
	}
	var parsed struct{}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Errorf("plist is not well-formed XML: %v", err)
	}
	if got := plistBinary([]byte(body)); got != h.Binary {
		t.Errorf("plistBinary = %q, want %q", got, h.Binary)
	}

	wantCalls := []string{
		"launchctl bootout gui/501/dev.sparkwing.crons",
		"launchctl bootstrap gui/501 " + path,
	}
	if !slices.Equal(exec.calls, wantCalls) {
		t.Errorf("exec calls = %v, want %v", exec.calls, wantCalls)
	}
}

func TestGeneratedFilesEscapeAwkwardValues(t *testing.T) {
	t.Run("darwin escapes xml", func(t *testing.T) {
		exec := &fakeExec{}
		h := darwinHost(t, exec)
		h.Binary = "/opt/tools & toys/sparkwing"
		h.LogPath = filepath.Join(h.Home, "logs & more", "crons.log")
		if _, err := Install(h); err != nil {
			t.Fatalf("Install: %v", err)
		}
		body := read(t, filepath.Join(AgentDir(h), h.plistName()))
		if strings.Contains(body, "& toys") {
			t.Errorf("plist carries a raw ampersand:\n%s", body)
		}
		if !strings.Contains(body, "/opt/tools &amp; toys/sparkwing") {
			t.Errorf("plist did not escape the binary path:\n%s", body)
		}
		var parsed struct{}
		if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("plist is not well-formed XML: %v", err)
		}
		if got := plistBinary([]byte(body)); got != h.Binary {
			t.Errorf("plistBinary = %q, want %q", got, h.Binary)
		}

		state, err := Status(h)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if state.Stale {
			t.Errorf("State.Stale = true, want the escaped path to round-trip: %+v", state)
		}
	})

	t.Run("linux quotes spaces and doubles percent", func(t *testing.T) {
		exec := &fakeExec{}
		h := linuxHost(t, exec)
		h.Binary = "/opt/my tools/sparkwing"
		h.PathEnv = "/opt/my tools:/usr/bin"
		h.SparkwingHome = "/srv/100% sparkwing"
		if _, err := Install(h); err != nil {
			t.Fatalf("Install: %v", err)
		}
		service := read(t, filepath.Join(UnitDir(h), h.serviceName()))
		for _, want := range []string{
			`ExecStart="/opt/my tools/sparkwing" crons tick`,
			`Environment="PATH=/opt/my tools:/usr/bin"`,
			`Environment="SPARKWING_HOME=/srv/100%% sparkwing"`,
		} {
			if !strings.Contains(service, want) {
				t.Errorf("service unit missing %q:\n%s", want, service)
			}
		}
		if got := execStartBinary(service); got != h.Binary {
			t.Errorf("execStartBinary = %q, want %q", got, h.Binary)
		}
		state, err := Status(h)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if state.Stale {
			t.Errorf("State.Stale = true, want the quoted path to round-trip: %+v", state)
		}
	})
}

func TestAForeignFileBlocksInstallAndSurvivesUninstall(t *testing.T) {
	tests := []struct {
		name string
		goos string
		// safety: on systemd, State names the timer whichever of the two files is foreign.
		foreign  func(Host) string
		reported func(Host) string
	}{
		{
			name:     "linux service",
			goos:     "linux",
			foreign:  func(h Host) string { return filepath.Join(UnitDir(h), h.serviceName()) },
			reported: func(h Host) string { return filepath.Join(UnitDir(h), h.timerName()) },
		},
		{
			name:     "linux timer",
			goos:     "linux",
			foreign:  func(h Host) string { return filepath.Join(UnitDir(h), h.timerName()) },
			reported: func(h Host) string { return filepath.Join(UnitDir(h), h.timerName()) },
		},
		{
			name:     "darwin plist",
			goos:     "darwin",
			foreign:  func(h Host) string { return filepath.Join(AgentDir(h), h.plistName()) },
			reported: func(h Host) string { return filepath.Join(AgentDir(h), h.plistName()) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeExec{}
			h := linuxHost(t, exec)
			if tc.goos == "darwin" {
				h = darwinHost(t, exec)
			}
			path := tc.foreign(h)
			handWritten := "[Unit]\nDescription=mine, not sparkwing's\n"
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(handWritten), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}

			state, err := Install(h)
			if err == nil {
				t.Fatal("Install overwrote a file sparkwing did not write")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %v, want it to name %s", err, path)
			}
			if !state.Foreign || state.Installed {
				t.Errorf("State = %+v, want foreign and not installed", state)
			}
			if state.Path != tc.reported(h) {
				t.Errorf("State.Path = %q, want %q", state.Path, tc.reported(h))
			}
			if len(exec.calls) != 0 {
				t.Errorf("Install ran commands despite refusing: %v", exec.calls)
			}

			state, err = Uninstall(h)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if !state.Foreign {
				t.Errorf("State.Foreign = false, want the foreign file reported: %+v", state)
			}
			if got := read(t, path); got != handWritten {
				t.Errorf("Uninstall changed a file it does not own: %q", got)
			}
			if len(exec.calls) != 0 {
				t.Errorf("Uninstall ran commands despite refusing: %v", exec.calls)
			}

			state, err = Status(h)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if !state.Foreign || state.Installed {
				t.Errorf("Status = %+v, want foreign and not installed", state)
			}
		})
	}
}

func TestUninstallRemovesManagedFilesAndRepeatsCleanly(t *testing.T) {
	tests := []struct {
		goos      string
		files     func(Host) []string
		wantCalls []string
	}{
		{
			goos: "linux",
			files: func(h Host) []string {
				return []string{filepath.Join(UnitDir(h), h.serviceName()), filepath.Join(UnitDir(h), h.timerName())}
			},
			wantCalls: []string{
				"systemctl --user show -p FragmentPath --value sparkwing-crons.timer",
				"systemctl --user disable --now sparkwing-crons.timer",
				"systemctl --user daemon-reload",
			},
		},
		{
			goos:      "darwin",
			files:     func(h Host) []string { return []string{filepath.Join(AgentDir(h), h.plistName())} },
			wantCalls: []string{"launchctl bootout gui/501/dev.sparkwing.crons"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			installer := &fakeExec{}
			h := linuxHost(t, installer)
			if tc.goos == "darwin" {
				h = darwinHost(t, installer)
			}
			if _, err := Install(h); err != nil {
				t.Fatalf("Install: %v", err)
			}

			remover := &fakeExec{}
			h.Exec = remover.run
			state, err := Uninstall(h)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if state.Installed || state.Enabled || state.Foreign {
				t.Errorf("State = %+v, want everything gone", state)
			}
			for _, p := range tc.files(h) {
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s still exists after uninstall (%v)", p, err)
				}
			}
			if !slices.Equal(remover.calls, tc.wantCalls) {
				t.Errorf("exec calls = %v, want %v", remover.calls, tc.wantCalls)
			}

			again := &fakeExec{}
			h.Exec = again.run
			state, err = Uninstall(h)
			if err != nil {
				t.Fatalf("second Uninstall: %v", err)
			}
			if state.Installed || state.Foreign {
				t.Errorf("State = %+v, want a no-op", state)
			}
			if len(again.calls) != 0 {
				t.Errorf("second Uninstall ran %v, want nothing to do", again.calls)
			}
		})
	}
}

func TestUninstallIgnoresAServiceManagerThatNeverLoadedTheJob(t *testing.T) {
	tests := []struct {
		goos   string
		prefix string
		out    string
	}{
		{"linux", "systemctl --user disable", "Failed to disable unit: Unit file sparkwing-crons.timer does not exist."},
		{"darwin", "launchctl bootout", "Boot-out failed: 3: No such process"},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			installer := &fakeExec{}
			h := linuxHost(t, installer)
			if tc.goos == "darwin" {
				h = darwinHost(t, installer)
			}
			if _, err := Install(h); err != nil {
				t.Fatalf("Install: %v", err)
			}
			remover := &fakeExec{}
			remover.fail(tc.prefix, tc.out)
			h.Exec = remover.run
			if _, err := Uninstall(h); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if _, err := os.Stat(filepath.Join(UnitDir(h), h.timerName())); tc.goos == "linux" && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("timer survived an ignorable failure (%v)", err)
			}
		})
	}
}

func TestEnableFailureIsReportedAndTheFilesStay(t *testing.T) {
	tests := []struct {
		goos    string
		prefix  string
		out     string
		wantErr string
		file    func(Host) string
	}{
		{
			goos: "linux", prefix: "systemctl --user enable",
			out:     "Failed to connect to bus: No medium found",
			wantErr: "No medium found",
			file:    func(h Host) string { return filepath.Join(UnitDir(h), h.timerName()) },
		},
		{
			goos: "darwin", prefix: "launchctl bootstrap",
			out:     "Bootstrap failed: 5: Input/output error",
			wantErr: "Input/output error",
			file:    func(h Host) string { return filepath.Join(AgentDir(h), h.plistName()) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			exec := &fakeExec{}
			exec.fail(tc.prefix, tc.out)
			h := linuxHost(t, exec)
			if tc.goos == "darwin" {
				h = darwinHost(t, exec)
			}
			state, err := Install(h)
			if err == nil {
				t.Fatal("Install: want an error when the service manager refuses")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to carry %q", err, tc.wantErr)
			}
			if !state.Installed || state.Enabled {
				t.Errorf("State = %+v, want the files written but not enabled", state)
			}
			if _, err := os.Stat(tc.file(h)); err != nil {
				t.Errorf("%s was rolled back: %v", tc.file(h), err)
			}
		})
	}
}

func TestUnsupportedGOOS(t *testing.T) {
	for _, goos := range []string{"windows", "plan9", ""} {
		t.Run("goos="+goos, func(t *testing.T) {
			h := Host{GOOS: goos, Home: t.TempDir(), Binary: "/usr/local/bin/sparkwing", Exec: (&fakeExec{}).run}
			for name, fn := range map[string]func(Host) (State, error){"Install": Install, "Uninstall": Uninstall, "Status": Status} {
				if _, err := fn(h); !errors.Is(err, ErrUnsupported) {
					t.Errorf("%s error = %v, want ErrUnsupported", name, err)
				}
			}
		})
	}
}

func TestInstallRejectsABinaryItCannotBake(t *testing.T) {
	tests := []struct {
		name   string
		binary string
	}{
		{"empty", ""},
		{"relative", "sparkwing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, goos := range []string{"linux", "darwin"} {
				exec := &fakeExec{}
				h := linuxHost(t, exec)
				if goos == "darwin" {
					h = darwinHost(t, exec)
				}
				h.Binary = tc.binary
				if _, err := Install(h); err == nil {
					t.Errorf("%s: Install accepted binary %q", goos, tc.binary)
				}
			}
		})
	}
}

func TestExecIsRequiredOnceACommandIsNeeded(t *testing.T) {
	h := linuxHost(t, &fakeExec{})
	h.Exec = nil
	if _, err := Install(h); err == nil || !strings.Contains(err.Error(), "Host.Exec") {
		t.Errorf("Install error = %v, want it to name Host.Exec", err)
	}
}

func TestLinuxStatusAndUninstallLeaveAStrangersUnitAlone(t *testing.T) {
	exec := &fakeExec{}
	h := linuxHost(t, exec)
	if _, err := Install(h); err != nil {
		t.Fatalf("install: %v", err)
	}
	exec.rules = append(exec.rules, rule{"systemctl --user show -p FragmentPath", "/elsewhere/systemd/user/sparkwing-crons.timer\n", nil})
	exec.calls = nil

	st, err := Status(h)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Installed || st.Enabled {
		t.Fatalf("status = %+v, want installed and not enabled", st)
	}
	if !strings.Contains(st.Detail, "/elsewhere/") {
		t.Fatalf("detail should name the loaded unit: %q", st.Detail)
	}
	for _, c := range exec.calls {
		if strings.Contains(c, "is-enabled") || strings.Contains(c, "is-active") {
			t.Fatalf("status asked systemd about a unit it does not own: %v", exec.calls)
		}
	}

	exec.calls = nil
	st, err = Uninstall(h)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, c := range exec.calls {
		if strings.Contains(c, "disable") {
			t.Fatalf("uninstall disabled a unit it does not own: %v", exec.calls)
		}
	}
	if !strings.Contains(st.Detail, "left alone") {
		t.Fatalf("detail = %q", st.Detail)
	}
	if _, err := os.Stat(filepath.Join(UnitDir(h), h.timerName())); !os.IsNotExist(err) {
		t.Fatalf("our timer file should be removed: %v", err)
	}
}

func TestASecondHomeGetsItsOwnTimerAndLeavesTheDefaultHomesAlone(t *testing.T) {
	for _, tc := range []struct {
		goos string
		host func(*testing.T, *fakeExec) Host
	}{
		{"linux", linuxHost},
		{"darwin", darwinHost},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			exec := &fakeExec{}
			first := tc.host(t, exec)
			second := first
			second.SparkwingHome = filepath.Join(first.Home, "work-home")
			if _, err := Install(first); err != nil {
				t.Fatalf("Install default home: %v", err)
			}
			secondState, err := Install(second)
			if err != nil {
				t.Fatalf("Install second home: %v", err)
			}
			firstState, err := Status(first)
			if err != nil {
				t.Fatalf("Status default home: %v", err)
			}
			if firstState.Path == secondState.Path {
				t.Fatalf("both homes installed to %s", firstState.Path)
			}
			if !firstState.Enabled || strings.Contains(firstState.Detail, "another Sparkwing home") {
				t.Errorf("default home after a second install = %+v, want its own timer still enabled", firstState)
			}
			if !strings.Contains(firstState.Path, "sparkwing-crons.timer") && !strings.HasSuffix(firstState.Path, "dev.sparkwing.crons.plist") {
				t.Errorf("default home path = %s, want the name existing installs use", firstState.Path)
			}
		})
	}
}

func TestStatusRefusesATimerThatTicksAnotherHome(t *testing.T) {
	for _, tc := range []struct {
		goos string
		host func(*testing.T, *fakeExec) Host
	}{
		{"linux", linuxHost},
		{"darwin", darwinHost},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			exec := &fakeExec{}
			h := tc.host(t, exec)
			other := h
			other.Home = t.TempDir()
			other.SparkwingHome = filepath.Join(other.Home, ".sparkwing")
			writeAt := h
			writeAt.SparkwingHome = ""
			writeAt.Env = map[string]string{homeEnv: other.SparkwingHome}
			if _, err := Install(writeAt); err != nil {
				t.Fatalf("Install: %v", err)
			}
			state, err := Status(h)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if state.Enabled || !strings.Contains(state.Detail, "another Sparkwing home") {
				t.Errorf("Status = %+v, want it reported as another home's timer", state)
			}
		})
	}
}

func TestCustomHomeRetiresOnlyItsManagedLegacyTimer(t *testing.T) {
	for _, tc := range []struct {
		goos string
		host func(*testing.T, *fakeExec) Host
	}{
		{"linux", linuxHost},
		{"darwin", darwinHost},
	} {
		for _, action := range []string{"install", "uninstall"} {
			for _, sameHome := range []bool{true, false} {
				t.Run(tc.goos+"/"+action+"/same="+strconv.FormatBool(sameHome), func(t *testing.T) {
					exec := &fakeExec{}
					h := tc.host(t, exec)
					h.SparkwingHome = filepath.Join(h.Home, "custom")
					legacy := h
					legacy.SparkwingHome = ""
					legacy.Env = map[string]string{homeEnv: h.SparkwingHome}
					if !sameHome {
						legacy.Env[homeEnv] = filepath.Join(h.Home, "someone-else")
					}
					if _, err := Install(legacy); err != nil {
						t.Fatalf("install legacy timer: %v", err)
					}
					legacyPath := filepath.Join(UnitDir(legacy), legacy.timerName())
					if tc.goos == "darwin" {
						legacyPath = filepath.Join(AgentDir(legacy), legacy.plistName())
					}
					if action == "install" {
						if _, err := Install(h); err != nil {
							t.Fatalf("install custom timer: %v", err)
						}
					} else if _, err := Uninstall(h); err != nil {
						t.Fatalf("uninstall custom timer: %v", err)
					}
					_, err := os.Stat(legacyPath)
					if sameHome && !errors.Is(err, os.ErrNotExist) {
						t.Errorf("matching legacy timer remains: %v", err)
					}
					if !sameHome && err != nil {
						t.Errorf("other home's legacy timer was removed: %v", err)
					}
				})
			}
		}
	}
}
