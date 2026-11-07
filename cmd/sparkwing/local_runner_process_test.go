package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

type runnerDiscoveryFixture struct {
	active localhostwire.LocalRunnerInstance
	read   func(string) (localhostwire.LocalRunnerInstance, error)
}

func (f runnerDiscoveryFixture) Active(context.Context) (localhostwire.LocalRunnerInstance, error) {
	if f.active.ID == "" {
		return f.active, &apierr.Error{Code: apierr.NotFound}
	}
	return f.active, nil
}

func (f runnerDiscoveryFixture) Read(_ context.Context, id string) (localhostwire.LocalRunnerInstance, error) {
	return f.read(id)
}

func TestLocalRunnerSupervisorReusesOnlyMatchingLiveBuild(t *testing.T) {
	for _, change := range []string{"same", "version", "build", "dead", "absent", "unknown"} {
		t.Run(change, func(t *testing.T) {
			old := localhostwire.LocalRunnerInstance{ID: "old", State: "active", Version: "v1", BuildIdentity: "build"}
			fixture := runnerDiscoveryFixture{active: old}
			supervisor := localRunnerProcessSupervisor{
				version: "v1", build: "build",
				alive: func(instance localhostwire.LocalRunnerInstance) (bool, error) {
					if change == "unknown" {
						return false, os.ErrPermission
					}
					return change != "dead" || instance.ID != "old", nil
				},
			}
			switch change {
			case "version":
				fixture.active.Version = "v0"
			case "build":
				fixture.active.BuildIdentity = "old-build"
			case "absent":
				fixture.active.ID = ""
			}
			var started string
			supervisor.start = func(_ context.Context, id string) error { started = id; return nil }
			fixture.read = func(id string) (localhostwire.LocalRunnerInstance, error) {
				if id != started || id == old.ID {
					t.Fatalf("startup read changed instance: %s", id)
				}
				return localhostwire.LocalRunnerInstance{ID: id, State: "active", Version: "v1", BuildIdentity: "build"}, nil
			}
			supervisor.registry = fixture
			err := supervisor.ensure(t.Context())
			if change == "unknown" {
				if !errors.Is(err, os.ErrPermission) || started != "" {
					t.Fatalf("unknown process triggered replacement: %s %v", started, err)
				}
				return
			}
			if err != nil || (started == "") != (change == "same") {
				t.Fatalf("reuse %s: started=%s err=%v", change, started, err)
			}
		})
	}
}

func TestLocalRunnerSupervisorDoesNotRetireOldRunnerOnFailedStartup(t *testing.T) {
	failure := errors.New("manager unavailable")
	supervisor := localRunnerProcessSupervisor{
		registry: runnerDiscoveryFixture{active: localhostwire.LocalRunnerInstance{ID: "old", BuildIdentity: "old-build"}},
		version:  "v1", build: "build", alive: func(localhostwire.LocalRunnerInstance) (bool, error) { return true, nil },
		start: func(context.Context, string) error { return failure },
	}
	if err := supervisor.ensure(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("startup failure: %v", err)
	}
}

func TestLocalRunnerSupervisorRejectsChangedReadyIdentity(t *testing.T) {
	supervisor := localRunnerProcessSupervisor{
		registry: runnerDiscoveryFixture{read: func(id string) (localhostwire.LocalRunnerInstance, error) {
			return localhostwire.LocalRunnerInstance{ID: id, Version: "v1", BuildIdentity: "other", State: "active"}, nil
		}},
		version: "v1", build: "build", alive: func(localhostwire.LocalRunnerInstance) (bool, error) { return true, nil },
		start: func(context.Context, string) error { return nil },
	}
	if err := supervisor.ensure(t.Context()); err == nil {
		t.Fatal("accepted a different executable")
	}
}

func TestPinnedLocalRunnerExecutableSurvivesInstallAndRejectsCorruption(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "installed")
	if err := os.WriteFile(source, []byte("first"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, build, err := pinLocalRunnerExecutable(home, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("next"), 0o700); err != nil {
		t.Fatal(err)
	}
	next, nextBuild, err := pinLocalRunnerExecutable(home, source)
	if err != nil || first == next || build == nextBuild {
		t.Fatalf("install did not preserve builds: %s %s %v", first, next, err)
	}
	data, err := os.ReadFile(first)
	if err != nil || string(data) != "first" {
		t.Fatalf("old image changed: %q %v", data, err)
	}
	if err := os.WriteFile(next, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pinLocalRunnerExecutable(home, source); err == nil {
		t.Fatal("reused corrupt image")
	}
}

func TestLocalRunnerSupervisorBoundsActivationWithoutKillingProcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := 0
		supervisor := localRunnerProcessSupervisor{
			registry: runnerDiscoveryFixture{read: func(string) (localhostwire.LocalRunnerInstance, error) {
				return localhostwire.LocalRunnerInstance{}, &apierr.Error{Code: apierr.NotFound}
			}},
			version: "v1", build: "build", alive: func(localhostwire.LocalRunnerInstance) (bool, error) { return true, nil },
			start: func(context.Context, string) error { started++; return nil },
		}
		start := time.Now()
		if err := supervisor.ensure(t.Context()); !errors.Is(err, context.DeadlineExceeded) || started != 1 || time.Since(start) != 45*time.Second {
			t.Fatalf("activation deadline: starts=%d elapsed=%s err=%v", started, time.Since(start), err)
		}
	})
}

func TestLocalRunnerSupervisorBacksOffFailedStartupWithoutStoppingController(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		starts := 0
		supervisor := localRunnerProcessSupervisor{
			registry: runnerDiscoveryFixture{}, version: "v1", build: "build",
			alive: func(localhostwire.LocalRunnerInstance) (bool, error) { return true, nil },
			start: func(context.Context, string) error { starts++; return errors.New("launchd domain unavailable") },
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		if err := supervisor.run(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("supervisor stopped before its controller: %v", err)
		}
		if starts != 7 {
			t.Fatalf("startup attempts = %d, want 7 (0, 2, 6, 14, 30, 62, 122s)", starts)
		}
	})
}
