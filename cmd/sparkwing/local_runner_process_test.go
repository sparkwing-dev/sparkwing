package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
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
	first, build, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("next"), 0o700); err != nil {
		t.Fatal(err)
	}
	next, nextBuild, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, "")
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
	if _, _, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, ""); err == nil {
		t.Fatal("reused corrupt image")
	}
}

func TestPinnedLocalRunnerExecutableReusesOnlyIdenticalBytes(t *testing.T) {
	for _, change := range []string{"unchanged", "same-size", "shorter", "longer"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			source := filepath.Join(home, "installed")
			original := strings.Repeat("first", 20000)
			if err := os.WriteFile(source, []byte(original), 0o700); err != nil {
				t.Fatal(err)
			}
			path, build, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, "")
			if err != nil {
				t.Fatal(err)
			}
			contents := original
			switch change {
			case "same-size":
				contents = original[:len(original)-1] + "x"
			case "shorter":
				contents = original[:len(original)-1]
			case "longer":
				contents = original + "x"
			}
			if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
				t.Fatal(err)
			}
			next, nextBuild, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, "")
			if change == "unchanged" {
				if err != nil || path != next || build != nextBuild {
					t.Fatalf("reuse = %s %s %v", next, nextBuild, err)
				}
			} else {
				if err == nil {
					t.Fatal("reused changed executable")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				next, nextBuild, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, build)
				if err != nil || next != path || nextBuild != build {
					t.Fatalf("restore missing image = %s %s %v", next, nextBuild, err)
				}
			}
			files, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("staged files survived: %v", files)
			}
		})
	}
}

func TestPinnedLocalRunnerReusesAcceptedImageWithoutDirectoryWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory modes do not remove write access")
	}
	home := t.TempDir()
	source := filepath.Join(home, "installed")
	if err := os.WriteFile(source, []byte("accepted"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, build, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(first)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	next, nextBuild, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, build)
	if err != nil || next != first || nextBuild != build {
		t.Fatalf("reuse without staging = %s %s %v", next, nextBuild, err)
	}
}

func TestPinnedLocalRunnerRefusesChangedAcceptedSource(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained=%t", retained), func(t *testing.T) {
			home := t.TempDir()
			source := filepath.Join(home, "installed")
			contents := []byte("accepted")
			sum := sha256.Sum256(contents)
			build := hex.EncodeToString(sum[:])
			if err := os.WriteFile(source, contents, 0o700); err != nil {
				t.Fatal(err)
			}
			if retained {
				path, _, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, build)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replaced"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			replacement := filepath.Join(home, "replacement")
			if err := os.WriteFile(replacement, []byte("replaced"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, source); err != nil {
				t.Fatal(err)
			}
			if _, _, err := engine.RetainExecutable(filepath.Join(home, "runners", "builds"), source, build); err == nil {
				t.Fatal("accepted source replacement")
			}
			files, err := os.ReadDir(filepath.Join(home, "runners", "builds"))
			if err != nil || len(files) != map[bool]int{false: 0, true: 1}[retained] {
				t.Fatalf("unexpected retained/staged files: %v %v", files, err)
			}
		})
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
