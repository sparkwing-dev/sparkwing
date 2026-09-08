package crontimer

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		Env:        map[string]string{"SPARKWING_HOME": filepath.Join(root, ".sparkwing")},
		LogPath:    filepath.Join(root, ".sparkwing", "crons.log"),
		UID:        1000,
		Exec:       e.run,
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
		Env:        map[string]string{"SPARKWING_HOME": filepath.Join(root, ".sparkwing")},
		LogPath:    filepath.Join(root, ".sparkwing", "crons.log"),
		UID:        501,
		Exec:       e.run,
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
	servicePath := filepath.Join(UnitDir(h), ServiceName)
	timerPath := filepath.Join(UnitDir(h), TimerName)
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
		"Unit=" + ServiceName,
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
	path := filepath.Join(AgentDir(h), PlistName)
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
		"<key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/bin:/bin</string>",
		"<key>SPARKWING_HOME</key>",
		"<key>StandardOutPath</key>\n  <string>" + h.LogPath + "</string>",
		"<key>StandardErrorPath</key>\n  <string>" + h.LogPath + "</string>",
		"<key>ProcessType</key>\n  <string>Background</string>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q:\n%s", want, body)
		}
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
		body := read(t, filepath.Join(AgentDir(h), PlistName))
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
		h.Env = map[string]string{"SPARKWING_HOME": "/srv/100% sparkwing"}
		if _, err := Install(h); err != nil {
			t.Fatalf("Install: %v", err)
		}
		service := read(t, filepath.Join(UnitDir(h), ServiceName))
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
			foreign:  func(h Host) string { return filepath.Join(UnitDir(h), ServiceName) },
			reported: func(h Host) string { return filepath.Join(UnitDir(h), TimerName) },
		},
		{
			name:     "linux timer",
			goos:     "linux",
			foreign:  func(h Host) string { return filepath.Join(UnitDir(h), TimerName) },
			reported: func(h Host) string { return filepath.Join(UnitDir(h), TimerName) },
		},
		{
			name:     "darwin plist",
			goos:     "darwin",
			foreign:  func(h Host) string { return filepath.Join(AgentDir(h), PlistName) },
			reported: func(h Host) string { return filepath.Join(AgentDir(h), PlistName) },
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
				return []string{filepath.Join(UnitDir(h), ServiceName), filepath.Join(UnitDir(h), TimerName)}
			},
			wantCalls: []string{
				"systemctl --user disable --now sparkwing-crons.timer",
				"systemctl --user daemon-reload",
			},
		},
		{
			goos:      "darwin",
			files:     func(h Host) []string { return []string{filepath.Join(AgentDir(h), PlistName)} },
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
			if _, err := os.Stat(filepath.Join(UnitDir(h), TimerName)); tc.goos == "linux" && !errors.Is(err, os.ErrNotExist) {
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
			file:    func(h Host) string { return filepath.Join(UnitDir(h), TimerName) },
		},
		{
			goos: "darwin", prefix: "launchctl bootstrap",
			out:     "Bootstrap failed: 5: Input/output error",
			wantErr: "Input/output error",
			file:    func(h Host) string { return filepath.Join(AgentDir(h), PlistName) },
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
