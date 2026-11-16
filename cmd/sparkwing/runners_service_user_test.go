//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type fakePrivilege struct {
	checkErr error
	runErr   error
	calls    [][]string
	stdin    [][]byte
	reply    func(args []string, stdin []byte) ([]byte, error)
	timeline *[]string
}

func (p *fakePrivilege) Check(context.Context) error {
	if p.timeline != nil {
		*p.timeline = append(*p.timeline, "sudo -v")
	}
	return p.checkErr
}

func (p *fakePrivilege) Run(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	p.calls = append(p.calls, args)
	p.stdin = append(p.stdin, stdin)
	if p.timeline != nil {
		*p.timeline = append(*p.timeline, "sudo "+strings.Join(args, " "))
	}
	if p.runErr != nil {
		return nil, p.runErr
	}
	return p.reply(args, stdin)
}

func swapServiceUserPrivilege(t *testing.T, p *fakePrivilege) {
	t.Helper()
	previous, previousBooted := serviceUserPrivilege, serviceUserBooted
	serviceUserPrivilege, serviceUserBooted = p, func() bool { return true }
	t.Cleanup(func() { serviceUserPrivilege, serviceUserBooted = previous, previousBooted })
}

func installedReply(account string) func([]string, []byte) ([]byte, error) {
	return func([]string, []byte) ([]byte, error) {
		home := "/var/lib/sparkwing/" + account
		return json.Marshal(serviceUserInstalled{
			Account: runnerservice.Account{Name: account, UID: 990, GID: 990, Home: home}, Created: true,
			Config:      home + "/.config/sparkwing/config.yaml",
			Service:     runnerServiceResult{Status: "started", Detail: "the runner service runs as " + account, Path: "/etc/systemd/system/sparkwing-runner-" + account + ".service"},
			DroppedPath: []string{"/home/korey/.asdf/shims"},
		})
	}
}

func TestRunnersAddServiceUserHandsTheCredentialToThePrivilegedStep(t *testing.T) {
	f := newRunnersFixture(t)
	p := &fakePrivilege{reply: installedReply("sw-ci"), timeline: &f.timeline}
	swapServiceUserPrivilege(t, p)
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--max-concurrent", "2", "--service-user", "sw-ci"}); err != nil {
				t.Fatal(err)
			}
		})
	})
	if len(f.enrolled) != 1 || f.enrolled[0].Personal {
		t.Fatalf("enrollment: %+v", f.enrolled)
	}
	if !slices.Equal(f.timeline[:1], []string{"sudo -v"}) {
		t.Fatalf("privilege was not checked before minting: %v", f.timeline)
	}
	if len(p.calls) != 1 || !slices.Equal(p.calls[0], []string{"runners", "install-service", "--service-user", "sw-ci", "--output", "json"}) {
		t.Fatalf("privileged calls = %q", p.calls)
	}
	var payload serviceUserEnrollment
	if err := json.Unmarshal(p.stdin[0], &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payload.Credential, f.tokens[0].Prefix) || payload.Runner.LocalAdmission == nil || *payload.Runner.LocalAdmission {
		t.Fatalf("payload = %+v", payload)
	}
	if !slices.Equal(payload.Runner.Repos, []string{"github.com/acme/api"}) || payload.Runner.MaxConcurrent == nil || *payload.Runner.MaxConcurrent != 2 || payload.RunnerBinary != f.binary {
		t.Fatalf("payload runner = %+v binary=%q", payload.Runner, payload.RunnerBinary)
	}
	if _, err := os.Stat(f.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the person's config was written: %v", err)
	}
	if slices.ContainsFunc(*f.calls, func(call string) bool { return strings.Contains(call, "--user") }) {
		t.Fatalf("a user service was installed: %q", *f.calls)
	}
	for _, want := range []string{"runs as sw-ci", "/var/lib/sparkwing/sw-ci/.config/sparkwing/config.yaml", "--max-concurrent is the only cap", "/home/korey/.asdf/shims", "runners delete --profile prod --service-user sw-ci"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, payload.Credential) || strings.Contains(stderr, payload.Credential) {
		t.Fatal("the credential reached the terminal")
	}
}

func TestRunnersAddServiceUserMintsNothingWithoutPrivilege(t *testing.T) {
	f := newRunnersFixture(t)
	swapServiceUserPrivilege(t, &fakePrivilege{checkErr: errors.New("sudo: a password is required")})
	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"})
	if err == nil || !strings.Contains(err.Error(), "password is required") || len(f.tokens) != 0 {
		t.Fatalf("err = %v tokens = %+v", err, f.tokens)
	}
}

func TestRunnersAddServiceUserRevokesWhenThePrivilegedStepFails(t *testing.T) {
	f := newRunnersFixture(t)
	swapServiceUserPrivilege(t, &fakePrivilege{runErr: errors.New("useradd: cannot lock /etc/passwd")})
	var err error
	captureStdout(t, func() {
		err = f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"})
	})
	if err == nil || !strings.Contains(err.Error(), "cannot lock") {
		t.Fatalf("err = %v", err)
	}
	if len(f.tokens) != 1 || f.tokens[0].RevokedAt == nil {
		t.Fatalf("minted credential left live: %+v", f.tokens)
	}
}

func TestRunnersAddServiceUserRefusesConflictsBeforeMinting(t *testing.T) {
	f := newRunnersFixture(t)
	swapServiceUserPrivilege(t, &fakePrivilege{reply: installedReply("sw-ci")})
	base := []string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api"}
	for _, extra := range [][]string{
		{"--service-user", "sw-ci", "--config", f.config},
		{"--service-user", "sw-ci", "--no-service"},
		{"--service-user", "sw-ci", "--launcher", "--reserved-for", "deploy"},
		{"--service-user", "sw-ci", "--platform", "kubernetes"},
		{"--service-user", "Bad Name"},
	} {
		if err := f.run(append(slices.Clone(base), extra...)); err == nil {
			t.Fatalf("%q accepted", extra)
		}
	}
	if len(f.tokens) != 0 {
		t.Fatalf("minted: %+v", f.tokens)
	}
}

func TestRunnersAddServiceUserNeedsASystemServiceManager(t *testing.T) {
	f := newRunnersFixture(t)
	swapServiceUserPrivilege(t, &fakePrivilege{reply: installedReply("sw-ci")})
	serviceUserBooted = func() bool { return false }
	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"})
	if err == nil || !strings.Contains(err.Error(), "systemd") || len(f.tokens) != 0 {
		t.Fatalf("err = %v tokens = %+v", err, f.tokens)
	}
	runnerServiceGOOS = "windows"
	err = f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"})
	if err == nil || len(f.tokens) != 0 {
		t.Fatalf("windows: err = %v tokens = %+v", err, f.tokens)
	}
}

func TestRunnersRemoveServiceUserStopsThroughThePrivilegedStepThenRevokes(t *testing.T) {
	f := newRunnersFixture(t)
	p := &fakePrivilege{reply: installedReply("sw-ci"), timeline: &f.timeline}
	swapServiceUserPrivilege(t, p)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"}); err != nil {
			t.Fatal(err)
		}
	})
	p.reply = func([]string, []byte) ([]byte, error) {
		return json.Marshal(serviceUserRemoved{Account: "sw-ci", Runner: "desk", Controller: "https://controller.example", TokenPrefix: f.tokens[0].Prefix, Detail: "stopped"})
	}
	f.timeline = nil
	out := captureStdout(t, func() {
		if err := f.run([]string{"remove", "--profile", "prod", "--service-user", "sw-ci"}); err != nil {
			t.Fatal(err)
		}
	})
	if !slices.Equal(f.timeline, []string{"sudo -v", "sudo runners uninstall-service --service-user sw-ci --output json", "revoke"}) {
		t.Fatalf("timeline = %q", f.timeline)
	}
	if f.tokens[0].RevokedAt == nil || !strings.Contains(out, "userdel") {
		t.Fatalf("tokens = %+v out = %s", f.tokens, out)
	}
}

func TestRunnersRemoveServiceUserRefusesAnotherControllersRunner(t *testing.T) {
	f := newRunnersFixture(t)
	p := &fakePrivilege{reply: installedReply("sw-ci")}
	swapServiceUserPrivilege(t, p)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"}); err != nil {
			t.Fatal(err)
		}
	})
	p.reply = func([]string, []byte) ([]byte, error) {
		return json.Marshal(serviceUserRemoved{Account: "sw-ci", Runner: "desk", Controller: "https://other.example", TokenPrefix: f.tokens[0].Prefix})
	}
	err := f.run([]string{"remove", "--profile", "prod", "--service-user", "sw-ci"})
	if err == nil || !strings.Contains(err.Error(), "other.example") || f.tokens[0].RevokedAt != nil {
		t.Fatalf("err = %v tokens = %+v", err, f.tokens)
	}
}

type installFixture struct {
	accounts []string
	runAs    []string
	host     runnerservice.AccountHost
	unitDir  string
	home     string
	exec     []string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	fx := &installFixture{unitDir: t.TempDir(), home: filepath.Join(t.TempDir(), "sw-ci")}
	if err := os.MkdirAll(fx.home, 0o700); err != nil {
		t.Fatal(err)
	}
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	fx.host = runnerservice.AccountHost{GOOS: "linux", HomeRoot: filepath.Dir(fx.home), InvokerUID: -1, PeopleRoots: []string{"/home/nobody-here"}, Exec: func(_ context.Context, name string, args ...string) (string, error) {
		fx.accounts = append(fx.accounts, name+" "+strings.Join(args, " "))
		switch name {
		case "getent":
			return "sw-ci:x:" + uid + ":" + gid + "::" + fx.home + ":/usr/sbin/nologin\n", nil
		case "id":
			return "sw-ci\n", nil
		}
		return "", nil
	}}
	previousEUID, previousHost, previousRunAs, previousSystem := serviceUserEUID, serviceUserAccountHost, serviceUserRunAs, serviceUserUnitDir
	serviceUserEUID = func() int { return 0 }
	serviceUserAccountHost = func(string) runnerservice.AccountHost { return fx.host }
	serviceUserUnitDir = func(string) string { return fx.unitDir }
	serviceUserRunAs = func(_ context.Context, account runnerservice.Account, args []string, _ []byte) ([]byte, error) {
		fx.runAs = append(fx.runAs, account.Name+" "+strings.Join(args[1:], " "))
		home := account.Home
		return json.Marshal(accountWritten{Config: home + "/.config/sparkwing/config.yaml", Binary: home + "/.local/bin/sparkwing-runner", StateHome: home + "/.sparkwing"})
	}
	previousStage := serviceUserStage
	serviceUserStage = func(string, string) (string, string, func(), error) {
		return "/stage/sparkwing", "/stage/sparkwing-runner", func() {}, nil
	}
	t.Cleanup(func() { serviceUserStage = previousStage })
	previousExec := serviceUserSystemExec
	serviceUserSystemExec = func(_ context.Context, name string, args ...string) (string, error) {
		fx.exec = append(fx.exec, name+" "+strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() {
		serviceUserEUID, serviceUserAccountHost, serviceUserRunAs, serviceUserUnitDir, serviceUserSystemExec = previousEUID, previousHost, previousRunAs, previousSystem, previousExec
	})
	return fx
}

func TestRunnersInstallServiceWritesFilesAsTheAccountAndInstallsItsUnit(t *testing.T) {
	fx := newInstallFixture(t)
	payload := serviceUserEnrollment{Credential: "swk_000000001_secret", RunnerBinary: "/usr/local/bin/sparkwing-runner", Path: "/home/korey/.asdf/shims:/usr/bin:/bin"}
	installed, err := installServiceUser(t.Context(), "linux", "sw-ci", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.runAs) != 1 || !strings.HasPrefix(fx.runAs[0], "sw-ci __runner-account-write --runner-binary ") {
		t.Fatalf("account writes = %q", fx.runAs)
	}
	body, err := os.ReadFile(filepath.Join(fx.unitDir, "sparkwing-runner-sw-ci.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"User=sw-ci", "ExecStart=:" + fx.home + "/.local/bin/sparkwing-runner start --config " + fx.home + "/.config/sparkwing/config.yaml", "Environment=PATH=" + fx.home + "/.local/bin:/usr/bin:/bin", "ProtectHome=yes"} {
		if !strings.Contains(string(body), line+"\n") {
			t.Fatalf("unit lacks %q:\n%s", line, body)
		}
	}
	if !slices.Equal(installed.DroppedPath, []string{"/home/korey/.asdf/shims"}) || installed.Account.Name != "sw-ci" || installed.Service.Status != "started" {
		t.Fatalf("installed = %+v", installed)
	}
}

func TestRunnersInstallServiceRefusesWithoutRoot(t *testing.T) {
	newInstallFixture(t)
	serviceUserEUID = func() int { return 1000 }
	_, err := installServiceUser(t.Context(), "linux", "sw-ci", serviceUserEnrollment{Credential: "x", RunnerBinary: "/usr/local/bin/sparkwing-runner"})
	if err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerAccountWriteStoresThePrivateConfigAndRunnerInItsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("SPARKWING_HOME", "")
	binary := filepath.Join(t.TempDir(), "sparkwing-runner")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	controller, admission := "https://controller.example", false
	name := "desk"
	payload := serviceUserEnrollment{Credential: "swk_000000001_secret", Runner: userconfig.RunnerConfig{Controller: &controller, HolderPrefix: &name, Repos: []string{"github.com/acme/api"}, LocalAdmission: &admission}}
	written, err := writeRunnerAccount(payload, binary)
	if err != nil {
		t.Fatal(err)
	}
	if written.Config != filepath.Join(home, ".config", "sparkwing", "config.yaml") || written.StateHome != filepath.Join(home, ".sparkwing") {
		t.Fatalf("written = %+v", written)
	}
	enrolled, err := loadEnrolledRunner(written.Config)
	if err != nil || enrolled.Token != payload.Credential || enrolled.LocalAdmission == nil || *enrolled.LocalAdmission {
		t.Fatalf("enrolled = %+v err=%v", enrolled, err)
	}
	info, err := os.Stat(written.Binary)
	if err != nil || info.Mode().Perm()&0o100 == 0 || filepath.Dir(written.Binary) != filepath.Join(home, ".local", "bin") {
		t.Fatalf("binary %s: %v %v", written.Binary, info, err)
	}
	read, err := readRunnerAccount()
	if err != nil || read.Runner != "desk" || read.Controller != controller || read.TokenPrefix != payload.Credential[:len(read.TokenPrefix)] || read.TokenPrefix == "" {
		t.Fatalf("read = %+v err=%v", read, err)
	}
}

func TestRunnerAccountWriteRefusesToRunAsRoot(t *testing.T) {
	previous := serviceUserEUID
	serviceUserEUID = func() int { return 0 }
	t.Cleanup(func() { serviceUserEUID = previous })
	if _, err := writeRunnerAccount(serviceUserEnrollment{}, "/bin/true"); err == nil {
		t.Fatal("wrote account files as root")
	}
}

func TestRunnersAddWarnsWhenASharedRunnerCanReadTheAccountsCredentials(t *testing.T) {
	cases := map[string]struct {
		args  []string
		seed  []string
		wants []string
		none  bool
	}{
		"shared on a daily driver": {args: []string{"--repos", "github.com/acme/api"}, seed: []string{".ssh/id_ed25519", ".aws/credentials"}, wants: []string{"~/.ssh", "~/.aws", "--service-user"}},
		"personal":                 {args: []string{"--personal"}, seed: []string{".ssh/id_ed25519"}, none: true},
		"dedicated account":        {args: []string{"--repos", "github.com/acme/api"}, none: true},
		"service user":             {args: []string{"--repos", "github.com/acme/api", "--service-user", "sw-ci"}, seed: []string{".ssh/id_ed25519"}, none: true},
		"several repositories":     {args: []string{"--repos", "github.com/acme/api,github.com/acme/web"}, wants: []string{"github.com/acme/api, github.com/acme/web", "read their secrets"}},
		"wildcard":                 {args: []string{"--repos", "github.com/acme/*"}, wants: []string{"github.com/acme/*", "read their secrets"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRunnersFixture(t)
			swapServiceUserPrivilege(t, &fakePrivilege{reply: installedReply("sw-ci")})
			home := t.TempDir()
			for _, rel := range tc.seed {
				path := filepath.Join(home, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			previous := runnerAccountHome
			runnerAccountHome = func() (string, error) { return home, nil }
			t.Cleanup(func() { runnerAccountHome = previous })
			args := append([]string{"--profile", "prod", "--runner", "desk", "--output", "json"}, tc.args...)
			if !slices.Contains(tc.args, "--service-user") {
				args = append(args, "--config", f.config, "--no-service")
			}
			var enrollment runnerEnrollment
			stderr := captureStderr(t, func() {
				var err error
				enrollment, _, err = prepareRunnerEnrollmentArgs(args)
				if err != nil {
					t.Fatal(err)
				}
			})
			if tc.none {
				if stderr != "" || len(enrollment.warnings) != 0 {
					t.Fatalf("unexpected warning: %q %q", stderr, enrollment.warnings)
				}
				return
			}
			for _, want := range tc.wants {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			out := captureStdout(t, func() {
				if err := enrollRunner(t.Context(), f, enrollment); err != nil {
					t.Fatal(err)
				}
			})
			var result runnerEnrollmentResult
			if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Warnings) == 0 {
				t.Fatalf("JSON warnings: %s %v", out, err)
			}
		})
	}
}

func TestServiceUserCommandsAreRegistered(t *testing.T) {
	for _, path := range []string{"runners install-service", "runners uninstall-service"} {
		inv, err := clisurface.Parse("sparkwing", append(strings.Fields(path), "--service-user", "sw-ci"))
		if err != nil || inv.Command.Path != "sparkwing "+path {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestRunnersUninstallServiceStopsTheServiceWhenTheAccountConfigIsGone(t *testing.T) {
	fx := newInstallFixture(t)
	if _, err := installServiceUser(t.Context(), "linux", "sw-ci", serviceUserEnrollment{Credential: "swk_000000001_secret", RunnerBinary: "/usr/local/bin/sparkwing-runner", Runner: userconfig.RunnerConfig{Controller: new("https://controller.example"), HolderPrefix: new("desk")}}); err != nil {
		t.Fatal(err)
	}
	fx.exec = nil
	serviceUserRunAs = func(context.Context, runnerservice.Account, []string, []byte) ([]byte, error) {
		return nil, errors.New("service account removed its config")
	}
	removed, err := uninstallServiceUser(t.Context(), "linux", "sw-ci")
	if !slices.Contains(fx.exec, "systemctl disable --now sparkwing-runner-sw-ci.service") {
		t.Fatalf("service was not stopped; commands=%q err=%v", fx.exec, err)
	}
	if err != nil || removed.TokenPrefix != "swk_000000001_secret"[:len(removed.TokenPrefix)] || removed.TokenPrefix == "" || removed.Runner != "desk" || removed.Controller != "https://controller.example" {
		t.Fatalf("removed = %+v err = %v, want the enrollment from the root-owned record", removed, err)
	}
}

func TestRunnersUninstallServiceStopsAndNamesTheRecoveryWithoutAnyRecord(t *testing.T) {
	fx := newInstallFixture(t)
	if _, err := installServiceUser(t.Context(), "linux", "sw-ci", serviceUserEnrollment{Credential: "swk_000000001_secret", RunnerBinary: "/usr/local/bin/sparkwing-runner"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(fx.host.HomeRoot, ".enrollments")); err != nil {
		t.Fatal(err)
	}
	fx.exec = nil
	serviceUserRunAs = func(context.Context, runnerservice.Account, []string, []byte) ([]byte, error) {
		return nil, errors.New("service account removed its config")
	}
	_, err := uninstallServiceUser(t.Context(), "linux", "sw-ci")
	if !slices.Contains(fx.exec, "systemctl disable --now sparkwing-runner-sw-ci.service") {
		t.Fatalf("service was not stopped; commands=%q err=%v", fx.exec, err)
	}
	if err == nil || !strings.Contains(err.Error(), "stopped and removed") || !strings.Contains(err.Error(), "tokens revoke") {
		t.Fatalf("err = %v, want the completed stop and the revoke step", err)
	}
}
