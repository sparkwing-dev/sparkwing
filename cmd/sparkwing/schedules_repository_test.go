package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const scheduleProject = `pipelines:
  - name: sweep
    entrypoint: Sweep
    on:
      schedule:
        - name: host
          cron: "*/15 * * * *"
          where: local
          args: { depth: shallow }
        - name: cloud
          cron: "0 4 * * *"
          where: controller
          catch_up: 2m
`

type scheduleRepositoryFixture struct {
	controllerwire.ScheduleRepositoryManager
	input   controllerwire.ScheduleRepoRequest
	deleted string
	calls   *[]string
}

func (f *scheduleRepositoryFixture) ReplaceSchedules(_ context.Context, input controllerwire.ScheduleRepoRequest) (controllerwire.ScheduleRepoWritten, error) {
	*f.calls = append(*f.calls, "replace")
	f.input = input
	return controllerwire.ScheduleRepoWritten{Schedules: []controllerwire.ScheduleView{{ID: "remote-id", Name: "repo/sweep/cloud"}}}, nil
}

func (f *scheduleRepositoryFixture) DeleteSchedulesForRepo(_ context.Context, repo string) (controllerwire.ScheduleRepoDeletion, error) {
	*f.calls = append(*f.calls, "delete-repository")
	f.deleted = repo
	return controllerwire.ScheduleRepoDeletion{RepoURL: repo, Removed: 1}, nil
}

type scheduleLifecycleFixture struct {
	wingd.LocalLifecycle
	calls  *[]string
	before func()
}

func (f scheduleLifecycleFixture) EnsureStarted(_ context.Context, _ wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	*f.calls = append(*f.calls, "start")
	if f.before != nil {
		f.before()
	}
	return wingd.LocalStatus{Running: true}, nil
}

type scheduleUnitsFixture struct {
	wingd.ManagedUnitManager
	calls *[]string
	units []wingd.ManagedUnit
}

func (f scheduleUnitsFixture) Inspect(context.Context) ([]wingd.ManagedUnit, error) {
	*f.calls = append(*f.calls, "inspect")
	return f.units, nil
}

func (f scheduleUnitsFixture) Refresh(context.Context, string, string) error {
	*f.calls = append(*f.calls, "refresh")
	return nil
}

func (f *scheduleFixture) DeleteSchedule(_ context.Context, id string) (controllerwire.ScheduleDeletion, error) {
	f.calls = append(f.calls, "delete:"+id)
	for name, row := range f.rows {
		if row.Schedule.ID == id {
			delete(f.rows, name)
		}
	}
	return controllerwire.ScheduleDeletion{ScheduleID: id, Deleted: true}, nil
}

type scheduleCommandFixture struct {
	command       scheduleCommand
	services      localScheduleServices
	manager       *scheduleFixture
	repository    *scheduleRepositoryFixture
	calls         []string
	registrations []localScheduleRegistration
}

func newScheduleCommandFixture(t *testing.T) *scheduleCommandFixture {
	t.Helper()
	f := &scheduleCommandFixture{manager: scheduleFake()}
	row := f.manager.rows["s1"]
	root := filepath.Join(t.TempDir(), "repo")
	home := filepath.Join(t.TempDir(), "state")
	row.Schedule.RepoPath = root
	f.manager.rows["s1"], f.manager.rows["check/nightly"] = row, row
	f.repository = &scheduleRepositoryFixture{calls: &f.calls}
	f.services = localScheduleServices{lifecycle: scheduleLifecycleFixture{calls: &f.calls},
		units: scheduleUnitsFixture{calls: &f.calls}, options: wingd.LocalStartOptions{Home: home, Executable: filepath.Join(home, "sparkwing"), Version: "v1.2.3"},
		register: func(_ context.Context, input localScheduleRegistration) (controllerwire.ScheduleRepoWritten, error) {
			f.calls = append(f.calls, "register")
			f.registrations = append(f.registrations, input)
			return controllerwire.ScheduleRepoWritten{Schedules: []controllerwire.ScheduleView{{ID: "local-id", Name: "repo/sweep/host"}}}, nil
		}, pin: func(_ context.Context, id string, _ bool) (controllerwire.ScheduleView, error) {
			f.calls = append(f.calls, "pin:"+id)
			return f.manager.rows[id].Schedule, nil
		}, tick: func(_ context.Context, dry bool) (controllerwire.TickView, error) {
			if !dry {
				t.Error("dry-run did not reach the controller tick")
			}
			f.calls = append(f.calls, "tick")
			return controllerwire.TickView{At: "2026-10-15T03:00:00Z", Version: "v1.2.3"}, nil
		}, installTimer: func(context.Context, wingd.LocalStartOptions) error {
			f.calls = append(f.calls, "install-timer")
			return nil
		}, uninstallTimer: func(context.Context) error {
			f.calls = append(f.calls, "uninstall-timer")
			return nil
		}}
	f.command = scheduleCommand{connect: func(_ context.Context, profile string) (scheduleConnection, error) {
		f.calls = append(f.calls, "connect:"+profile)
		return scheduleConnection{manager: f.manager, repositories: f.repository, close: func() { f.calls = append(f.calls, "close") }}, nil
	}, local: func(context.Context, string) (localScheduleServices, error) { return f.services, nil },
		roots: func(bool, bool) ([]string, error) { return []string{root}, nil },
		load:  func(string) (*projectconfig.Config, error) { return projectconfig.Parse([]byte(scheduleProject)) },
		source: func(_ context.Context, _ string, follow bool) (controllerwire.ScheduleRepoRequest, error) {
			input := controllerwire.ScheduleRepoRequest{RepoURL: "https://example.com/repo.git", Branch: "main", Follow: follow}
			if !follow {
				input.SHA = strings.Repeat("a", 40)
			}
			return input, nil
		}, environment: func() []string { return []string{"PATH=/bin", "BUILD_FLAG=captured"} }, goos: "linux"}
	return f
}

func TestScheduleInstallUsesRepositoryContractAndKeepsPinnedSource(t *testing.T) {
	f := newScheduleCommandFixture(t)
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"install", "--profile", "prod", "-o", "json"}, &out, f.command); err != nil {
		t.Fatal(err)
	}
	input := f.repository.input
	if input.SHA != strings.Repeat("a", 40) || input.Follow || len(input.Schedules) != 1 || input.Schedules[0].Name != "cloud" || input.Schedules[0].CatchUp != "2m" || !strings.Contains(out.String(), "sweep/host") {
		t.Fatalf("registration: %+v %s", input, out.String())
	}
	if !reflect.DeepEqual(f.calls, []string{"connect:prod", "replace", "close"}) {
		t.Fatalf("unexpected local effect: %v", f.calls)
	}
}

func TestScheduleInstallRejectsUnknownSelectorsAndMissingProjectBeforeConnecting(t *testing.T) {
	for _, missing := range []bool{true, false} {
		f := newScheduleCommandFixture(t)
		if missing {
			f.command.load = func(string) (*projectconfig.Config, error) { return nil, errors.New("project disappeared") }
		}
		var out bytes.Buffer
		err := runScheduleCommand(t.Context(), []string{"install", "--profile", "prod", "--schedule", "unknown"}, &out, f.command)
		if err == nil || len(f.calls) != 0 || out.Len() != 0 {
			t.Fatalf("incomplete declaration changed schedules: %v %v %s", err, f.calls, out.String())
		}
	}
	root := t.TempDir()
	if _, err := loadScheduleProject(root); err == nil {
		t.Fatal("a missing declaration was accepted as an empty replacement")
	}
}

func TestScheduleUninstallDoesNotNeedProjectFilesOrAPinnedCommit(t *testing.T) {
	f := newScheduleCommandFixture(t)
	f.command.load = func(string) (*projectconfig.Config, error) {
		t.Fatal("uninstall read project declarations")
		return nil, nil
	}
	f.command.source = func(context.Context, string, bool) (controllerwire.ScheduleRepoRequest, error) {
		return controllerwire.ScheduleRepoRequest{RepoURL: "https://example.com/repo.git"}, nil
	}
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"uninstall", "--profile", "prod", "-o", "plain"}, &out, f.command); err != nil || f.repository.deleted == "" || out.String() != "https://example.com/repo.git\n" {
		t.Fatalf("uninstall: %v %s", err, out.String())
	}
}

func TestLocalScheduleInstallCapturesBeforeStartingAndUsesManagedTimers(t *testing.T) {
	f := newScheduleCommandFixture(t)
	environment := []string{"CAPTURE=install"}
	f.command.environment = func() []string { return environment }
	f.services.lifecycle = scheduleLifecycleFixture{calls: &f.calls, before: func() { environment[0] = "CAPTURE=daemon" }}
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"install", "--no-prove", "-o", "plain"}, &out, f.command); err != nil {
		t.Fatal(err)
	}
	if len(f.registrations) != 1 || !reflect.DeepEqual(f.registrations[0].Environment, []string{"CAPTURE=install"}) || !f.registrations[0].Follow || f.registrations[0].Prove || f.registrations[0].Schedules[0].Name != "host" || out.String() != "local-id\n" {
		t.Fatalf("captured registration: %+v %s", f.registrations, out.String())
	}
	if !reflect.DeepEqual(f.calls, []string{"inspect", "start", "register", "install-timer"}) {
		t.Fatalf("installation ordering: %v", f.calls)
	}
}

func TestLocalScheduleBindingsRefuseBeforeControllerOrMutationEffects(t *testing.T) {
	for _, action := range []string{"install", "pin", "timer", "remove-timer", "tick"} {
		t.Run(action, func(t *testing.T) {
			f := newScheduleCommandFixture(t)
			args := []string{"install"}
			switch action {
			case "install":
				f.services.register = nil
			case "pin":
				f.services.pin = nil
				args = []string{"set", "--schedule", "s1", "--pin"}
			case "timer":
				f.services.installTimer = nil
			case "remove-timer":
				f.services.uninstallTimer = nil
				f.services.units = scheduleUnitsFixture{calls: &f.calls, units: []wingd.ManagedUnit{{Kind: wingd.ManagedSchedule, Home: f.services.options.Home}}}
				args = []string{"uninstall"}
			case "tick":
				f.services.tick = nil
			}
			var out bytes.Buffer
			var err error
			if action == "tick" {
				err = runScheduleTickCommand(t.Context(), []string{"--home", f.services.options.Home}, &out, f.command.local)
			} else {
				err = runScheduleCommand(t.Context(), args, &out, f.command)
			}
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != apierr.PolicyMissing || out.Len() != 0 {
				t.Fatalf("missing binding: %v %s", err, out.String())
			}
			for _, call := range f.calls {
				if call != "inspect" {
					t.Fatalf("missing binding caused %s", call)
				}
			}
		})
	}
}

func TestLocalSchedulePinResolvesAllSelectorsAndDeduplicates(t *testing.T) {
	for _, names := range []string{"s1,missing", "s1,check/nightly"} {
		f := newScheduleCommandFixture(t)
		var out bytes.Buffer
		err := runScheduleCommand(t.Context(), []string{"set", "--schedule", names, "--pin", "-o", "plain"}, &out, f.command)
		if strings.Contains(names, "missing") {
			if err == nil || strings.Contains(strings.Join(f.calls, ","), "pin:") || out.Len() != 0 {
				t.Fatalf("partial pin: %v %v %s", err, f.calls, out.String())
			}
		} else if err != nil || strings.Count(strings.Join(f.calls, ","), "pin:s1") != 1 || out.String() != "s1\n" {
			t.Fatalf("alias pin: %v %v %s", err, f.calls, out.String())
		}
	}
}

func TestScheduleTickHonorsCapturedHomeAndDryRun(t *testing.T) {
	for _, mode := range []string{"same", "different", "relative"} {
		f := newScheduleCommandFixture(t)
		home := f.services.options.Home
		if mode == "different" {
			home = filepath.Join(t.TempDir(), "different")
		} else if mode == "relative" {
			home = "relative"
		}
		var out bytes.Buffer
		err := runScheduleTickCommand(t.Context(), []string{"--home", home, "--dry-run", "-o", "json"}, &out, f.command.local)
		if mode == "same" {
			if err != nil || !reflect.DeepEqual(f.calls, []string{"start", "tick"}) || !strings.Contains(out.String(), "2026-10-15T03:00:00Z") {
				t.Fatalf("tick: %v %v %s", err, f.calls, out.String())
			}
		} else if err == nil || len(f.calls) != 0 || out.Len() != 0 {
			t.Fatalf("wrong home evaluated: %v %v %s", err, f.calls, out.String())
		}
	}
}

func TestScheduleTickResolvesTheExplicitHomeBeforeStarting(t *testing.T) {
	f := newScheduleCommandFixture(t)
	var home string
	explicit := filepath.Join(t.TempDir(), "explicit-state")
	resolve := func(_ context.Context, requested string) (localScheduleServices, error) {
		home = requested
		services := f.services
		services.options.Home = requested
		return services, nil
	}
	var out bytes.Buffer
	if err := runScheduleTickCommand(t.Context(), []string{"--home", explicit, "--dry-run"}, &out, resolve); err != nil || home != explicit || !reflect.DeepEqual(f.calls, []string{"start", "tick"}) {
		t.Fatalf("captured home: %s %v %v", home, f.calls, err)
	}
}

func TestScheduleInstallChecksEveryRepositoryBeforeStarting(t *testing.T) {
	f := newScheduleCommandFixture(t)
	f.command.roots = func(bool, bool) ([]string, error) { return []string{"/first", "/broken"}, nil }
	f.command.load = func(root string) (*projectconfig.Config, error) {
		if root == "/broken" {
			return nil, errors.New("invalid declarations")
		}
		return projectconfig.Parse([]byte(scheduleProject))
	}
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"install", "--all-repos"}, &out, f.command); err == nil || len(f.calls) != 0 || len(f.registrations) != 0 || out.Len() != 0 {
		t.Fatalf("partial fleet install: %v %v %s", err, f.calls, out.String())
	}
}

func TestScheduleInstallRefreshesAnExistingManagedTimer(t *testing.T) {
	f := newScheduleCommandFixture(t)
	f.services.units = scheduleUnitsFixture{calls: &f.calls, units: []wingd.ManagedUnit{{Kind: wingd.ManagedSchedule, Home: f.services.options.Home}}}
	f.services.installTimer = nil
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"install"}, &out, f.command); err != nil || !reflect.DeepEqual(f.calls, []string{"inspect", "start", "register", "refresh"}) {
		t.Fatalf("managed refresh: %v %v", f.calls, err)
	}
}

func TestLocalScheduleInstallWithoutDeclarationsDoesNotInstallATimer(t *testing.T) {
	f := newScheduleCommandFixture(t)
	f.command.load = func(string) (*projectconfig.Config, error) { return &projectconfig.Config{}, nil }
	f.services.units, f.services.installTimer = nil, nil
	f.services.register = func(_ context.Context, request localScheduleRegistration) (controllerwire.ScheduleRepoWritten, error) {
		f.calls = append(f.calls, "register")
		if request.Schedules == nil || len(request.Schedules) != 0 {
			t.Fatalf("empty replacement was lost: %+v", request)
		}
		return controllerwire.ScheduleRepoWritten{}, nil
	}
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"install"}, &out, f.command); err != nil || !reflect.DeepEqual(f.calls, []string{"start", "register"}) {
		t.Fatalf("empty declaration installed a timer: %v %v", f.calls, err)
	}
}

func TestScheduleUninstallUsesManagerAndRemovesLastNativeTimer(t *testing.T) {
	f := newScheduleCommandFixture(t)
	f.services.units = scheduleUnitsFixture{calls: &f.calls, units: []wingd.ManagedUnit{{Kind: wingd.ManagedSchedule, Home: f.services.options.Home}}}
	var out bytes.Buffer
	if err := runScheduleCommand(t.Context(), []string{"uninstall", "-o", "plain"}, &out, f.command); err != nil || out.String() != "s1\n" || !reflect.DeepEqual(f.manager.calls, []string{"delete:s1"}) {
		t.Fatalf("uninstall: %v %v %s", err, f.manager.calls, out.String())
	}
	if !reflect.DeepEqual(f.calls, []string{"inspect", "start", "connect:", "uninstall-timer", "close"}) {
		t.Fatalf("timer removal: %v", f.calls)
	}
}

func gitInRepo(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestScheduleProjectValidationKeepsNamedEntriesAndRefusesLegacyShape(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, projectconfig.Filename)
	if err := os.WriteFile(file, []byte(scheduleProject), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadScheduleProject(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := selectScheduleDeclarations(cfg, []string{"sweep", "sweep/host"}, "local")
	if err != nil || len(rows) != 1 || rows[0].Args["depth"] != "shallow" {
		t.Fatalf("selection: %+v %v", rows, err)
	}
	if err := os.WriteFile(file, []byte("pipelines:\n  - name: x\n    entrypoint: X\n    on:\n      schedule: { cron: '* * * * *', where: local }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScheduleProject(root); err == nil {
		t.Fatal("legacy schedule mapping survived the hard cut")
	}
}
