//go:build unix

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type fakeAccounts struct {
	calls   []string
	passwd  map[string]string
	dscl    map[string]string
	groups  map[string]string
	homes   string
	nextUID int
	fail    map[string]string
}

func (f *fakeAccounts) run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	for prefix, out := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return out, errors.New("exit status 1")
		}
	}
	switch {
	case name == "getent":
		entry, ok := f.passwd[args[1]]
		if !ok {
			return "", errors.New("exit status 2")
		}
		return entry + "\n", nil
	case name == "useradd":
		user := args[len(args)-1]
		home := args[slices.Index(args, "--home-dir")+1]
		if err := os.MkdirAll(home, 0o755); err != nil {
			return err.Error(), err
		}
		uid := strconv.Itoa(f.nextUID)
		f.passwd[user] = user + ":x:" + uid + ":" + uid + ":Sparkwing runner:" + home + ":/usr/sbin/nologin"
		return "", nil
	case name == "id":
		return f.groups[args[1]] + "\n", nil
	case name == "dscl" && args[1] == "-read":
		record, ok := f.dscl[strings.TrimPrefix(args[2], "/Users/")]
		if !ok {
			return "<dscl_cmd> DS Error: -14136 (eDSRecordNotFound)", errors.New("exit status 56")
		}
		return record, nil
	case name == "dscl" && args[1] == "-list" && args[2] == "/Users":
		return "root 0\n_www 70\nkorey 501\n_sw 400\n", nil
	case name == "dscl" && args[1] == "-list":
		return "wheel 0\nstaff 20\n_sw 400\n", nil
	case name == "dscl" && args[1] == "-create" && len(args) == 5 && args[3] == "NFSHomeDirectory":
		user := strings.TrimPrefix(args[2], "/Users/")
		f.dscl[user] = "NFSHomeDirectory: " + args[4] + "\nPrimaryGroupID: 401\nUniqueID: 401\n"
	}
	return "", nil
}

func newFakeAccounts(t *testing.T) (*fakeAccounts, AccountHost) {
	t.Helper()
	f := &fakeAccounts{passwd: map[string]string{}, dscl: map[string]string{}, groups: map[string]string{}, homes: t.TempDir(), nextUID: os.Getuid(), fail: map[string]string{}}
	return f, AccountHost{GOOS: "linux", HomeRoot: f.homes, InvokerName: "korey", InvokerUID: 4242, PeopleRoots: []string{"/home/pat-test", "/Users", "/home/sam"}, Exec: f.run, Chown: func(string, int, int) error { return nil }}
}

func TestEnsureAccountCreatesAPrivateSystemUserOnLinux(t *testing.T) {
	f, host := newFakeAccounts(t)
	f.nextUID = os.Getuid()
	host.InvokerUID = -1
	f.groups["sw-ci"] = "sw-ci"
	account, created, err := EnsureAccount(t.Context(), host, "sw-ci")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(f.homes, "sw-ci")
	if !created || account.Name != "sw-ci" || account.Home != home {
		t.Fatalf("account = %+v created=%v", account, created)
	}
	want := "useradd --system --user-group --create-home --home-dir " + home + " --shell /usr/sbin/nologin --comment Sparkwing runner sw-ci"
	if !slices.Contains(f.calls, want) {
		t.Fatalf("calls = %q, want %q", f.calls, want)
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %v, %v; want 0700", info.Mode(), err)
	}
}

func TestEnsureAccountCreatesAHiddenUserOnDarwin(t *testing.T) {
	f, host := newFakeAccounts(t)
	host.GOOS = "darwin"
	host.InvokerUID = -1
	f.groups["_ci"] = "_ci"
	var chowned []string
	host.Chown = func(path string, uid, gid int) error {
		chowned = append(chowned, path+" "+strconv.Itoa(uid)+":"+strconv.Itoa(gid))
		return nil
	}
	_, _, err := EnsureAccount(t.Context(), host, "_ci")
	if err == nil || !strings.Contains(err.Error(), "belongs to uid") {
		t.Fatalf("err = %v, want the ownership refusal: uid 401 cannot own a temporary directory", err)
	}
	home := filepath.Join(f.homes, "_ci")
	for _, want := range []string{
		"dscl . -create /Groups/_ci PrimaryGroupID 401",
		"dscl . -create /Users/_ci UniqueID 401",
		"dscl . -create /Users/_ci UserShell /usr/bin/false",
		"dscl . -create /Users/_ci NFSHomeDirectory " + home,
		"dscl . -create /Users/_ci IsHidden 1",
		"dscl . -create /Users/_ci Password *",
	} {
		if !slices.Contains(f.calls, want) {
			t.Fatalf("calls = %q, missing %q", f.calls, want)
		}
	}
	if !slices.Equal(chowned, []string{home + " 401:401"}) {
		t.Fatalf("chown = %q", chowned)
	}
}

func TestEnsureAccountRefusesAccountsThatCanReachThePerson(t *testing.T) {
	cases := map[string]struct {
		entry, groups, want string
		home                bool
	}{
		"root":        {entry: "toor:x:0:0::%s:/bin/sh", want: "it is root", home: true},
		"invoker":     {entry: "korey:x:4100:4100::%s:/bin/zsh", want: "your own account", home: true},
		"invoker uid": {entry: "alias:x:4242:4242::%s:/bin/zsh", want: "your own account", home: true},
		"sudo":        {entry: "ops:x:%d:%d::%s:/bin/sh", groups: "ops sudo", want: "sudo group", home: true},
		"docker":      {entry: "ops:x:%d:%d::%s:/bin/sh", groups: "ops docker", want: "docker group", home: true},
		"person":      {entry: "pat:x:1200:1200::/home/sam/pat:/bin/bash", want: "people's homes"},
		"users":       {entry: "pat:x:1200:1200::/Users/pat:/bin/bash", want: "people's homes"},
		"root home":   {entry: "svc:x:1200:1200::/:/bin/false", want: "not a private directory"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, host := newFakeAccounts(t)
			entry := tc.entry
			if tc.home {
				home := t.TempDir()
				switch strings.Count(entry, "%") {
				case 1:
					entry = strings.Replace(entry, "%s", home, 1)
				case 3:
					entry = strings.Replace(strings.Replace(strings.Replace(entry, "%d", strconv.Itoa(os.Getuid()), 1), "%d", strconv.Itoa(os.Getgid()), 1), "%s", home, 1)
				}
			}
			user := strings.SplitN(entry, ":", 2)[0]
			f.passwd[user] = entry
			f.groups[user] = tc.groups
			_, _, err := EnsureAccount(t.Context(), host, user)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if slices.ContainsFunc(f.calls, func(call string) bool { return strings.HasPrefix(call, "useradd") }) {
				t.Fatalf("an existing account was recreated: %q", f.calls)
			}
		})
	}
}

func TestEnsureAccountReusesASuitableAccount(t *testing.T) {
	f, host := newFakeAccounts(t)
	home := t.TempDir()
	f.passwd["sw-ci"] = "sw-ci:x:" + strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()) + "::" + home + ":/usr/sbin/nologin"
	f.groups["sw-ci"] = "sw-ci"
	host.InvokerUID = -1
	account, created, err := EnsureAccount(t.Context(), host, "sw-ci")
	if err != nil || created || account.Home != home {
		t.Fatalf("account = %+v created=%v err=%v", account, created, err)
	}
}

func TestValidAccountNameRefusesNamesNativeToolsMisread(t *testing.T) {
	for _, name := range []string{"", "Root", "-x", "a b", "x/../y", strings.Repeat("a", 32)} {
		if ValidAccountName(name) == nil {
			t.Fatalf("%q accepted", name)
		}
	}
	for _, name := range []string{"sparkwing-runner", "_sw", "ci2"} {
		if err := ValidAccountName(name); err != nil {
			t.Fatalf("%q refused: %v", name, err)
		}
	}
}

func systemHost(t *testing.T, goos string, exec *fakeExec) SystemHost {
	t.Helper()
	return SystemHost{
		GOOS:       goos,
		Account:    Account{Name: "sw-ci", UID: 990, GID: 990, Home: "/var/lib/sparkwing/sw-ci"},
		UnitDir:    t.TempDir(),
		Binary:     "/var/lib/sparkwing/sw-ci/.local/bin/sparkwing-runner",
		ConfigPath: "/var/lib/sparkwing/sw-ci/.config/sparkwing/config.yaml",
		StateHome:  "/var/lib/sparkwing/sw-ci/.sparkwing",
		LogPath:    "/Library/Logs/sparkwing-runner-sw-ci.log",
		Path:       "/var/lib/sparkwing/sw-ci/.local/bin:/usr/bin:/bin",
		Exec:       exec.run,
	}
}

func TestInstallSystemWritesASandboxedUnitForTheAccount(t *testing.T) {
	exec := &fakeExec{}
	h := systemHost(t, "linux", exec)
	state, err := InstallSystem(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.UnitDir, "sparkwing-runner-sw-ci.service")
	if state.Path != path || !state.Running {
		t.Fatalf("state = %+v", state)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"User=sw-ci", "Group=sw-ci",
		"ExecStart=:/var/lib/sparkwing/sw-ci/.local/bin/sparkwing-runner start --config /var/lib/sparkwing/sw-ci/.config/sparkwing/config.yaml",
		"Environment=HOME=/var/lib/sparkwing/sw-ci", "Environment=SPARKWING_HOME=/var/lib/sparkwing/sw-ci/.sparkwing",
		"WorkingDirectory=/var/lib/sparkwing/sw-ci/.sparkwing",
		"ProtectHome=yes", "NoNewPrivileges=yes", "PrivateTmp=yes", "ProtectSystem=full", "UMask=0077",
		"WantedBy=multi-user.target",
	} {
		if !slices.Contains(strings.Split(string(body), "\n"), line) {
			t.Fatalf("unit lacks %q:\n%s", line, body)
		}
	}
	want := []string{
		"systemctl show -p FragmentPath --value sparkwing-runner-sw-ci.service",
		"systemctl daemon-reload",
		"systemctl enable sparkwing-runner-sw-ci.service",
		"systemctl restart sparkwing-runner-sw-ci.service",
	}
	if !slices.Equal(exec.calls, want) {
		t.Fatalf("calls = %q, want %q", exec.calls, want)
	}
	if slices.ContainsFunc(exec.calls, func(call string) bool { return strings.Contains(call, "--user") }) {
		t.Fatalf("a system unit used the user manager: %q", exec.calls)
	}
}

func TestInstallSystemWritesADaemonForTheAccountOnDarwin(t *testing.T) {
	exec := &fakeExec{fail: map[string]string{"launchctl print": "Could not find service"}}
	h := systemHost(t, "darwin", exec)
	state, err := InstallSystem(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.UnitDir, "com.sparkwing.runner.sw-ci.plist")
	body, err := os.ReadFile(path)
	if err != nil || state.Path != path {
		t.Fatalf("state = %+v err=%v", state, err)
	}
	for _, fragment := range []string{
		"<key>UserName</key>\n  <string>sw-ci</string>",
		"<key>GroupName</key>\n  <string>sw-ci</string>",
		"<key>Label</key>\n  <string>com.sparkwing.runner.sw-ci</string>",
		"<key>HOME</key>\n    <string>/var/lib/sparkwing/sw-ci</string>",
		"<key>StandardOutPath</key>\n  <string>/Library/Logs/sparkwing-runner-sw-ci.log</string>",
	} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("plist lacks %q:\n%s", fragment, body)
		}
	}
	want := []string{"launchctl print system/com.sparkwing.runner.sw-ci", "launchctl bootstrap system " + path}
	if !slices.Equal(exec.calls, want) {
		t.Fatalf("calls = %q, want %q", exec.calls, want)
	}
}

func TestInstallSystemLeavesAForeignUnitAlone(t *testing.T) {
	exec := &fakeExec{}
	h := systemHost(t, "linux", exec)
	path := filepath.Join(h.UnitDir, "sparkwing-runner-sw-ci.service")
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSystem(t.Context(), h); err == nil || !strings.Contains(err.Error(), "not written by sparkwing") {
		t.Fatalf("err = %v", err)
	}
	if body, _ := os.ReadFile(path); string(body) != "[Service]\nExecStart=/bin/true\n" || len(exec.calls) != 0 {
		t.Fatalf("foreign unit touched: %q %q", body, exec.calls)
	}
}

func TestUninstallSystemStopsThenRemovesTheUnit(t *testing.T) {
	exec := &fakeExec{}
	h := systemHost(t, "linux", exec)
	if _, err := InstallSystem(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	exec.calls = nil
	state, err := UninstallSystem(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit remains: %v", err)
	}
	want := []string{
		"systemctl show -p FragmentPath --value sparkwing-runner-sw-ci.service",
		"systemctl disable --now sparkwing-runner-sw-ci.service",
		"systemctl daemon-reload",
	}
	if !slices.Equal(exec.calls, want) {
		t.Fatalf("calls = %q, want %q", exec.calls, want)
	}
}

func TestUninstallSystemKeepsTheUnitWhenStoppingFails(t *testing.T) {
	exec := &fakeExec{}
	h := systemHost(t, "linux", exec)
	if _, err := InstallSystem(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	exec.fail = map[string]string{"systemctl disable": "Failed to disable unit: Access denied"}
	state, err := UninstallSystem(t.Context(), h)
	if err == nil {
		t.Fatal("stop failure reported success")
	}
	if _, statErr := os.Stat(state.Path); statErr != nil {
		t.Fatalf("unit removed although the service may still run: %v", statErr)
	}
}

func TestSystemServicesRefuseOtherPlatforms(t *testing.T) {
	h := systemHost(t, "windows", &fakeExec{})
	if _, err := InstallSystem(t.Context(), h); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestServicePathDropsEntriesUnderPeoplesHomes(t *testing.T) {
	kept, dropped := ServicePath("/home/korey/.asdf/installs/golang/1.26/go/bin:/usr/local/go/bin:relative:/usr/bin:/Users/k/bin:/root/bin", "/var/lib/sparkwing/sw-ci/.local/bin")
	if kept != "/var/lib/sparkwing/sw-ci/.local/bin:/usr/local/go/bin:/usr/bin" {
		t.Fatalf("kept = %q", kept)
	}
	if !slices.Equal(dropped, []string{"/home/korey/.asdf/installs/golang/1.26/go/bin", "/Users/k/bin", "/root/bin"}) {
		t.Fatalf("dropped = %q", dropped)
	}
}
