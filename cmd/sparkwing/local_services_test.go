package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLocalServicesRejectInvalidAdmissionBeforeConstruction(t *testing.T) {
	for _, body := range []string{"admission:\n  mode: broken\n", "admission:\n  budget: broken\n"} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SPARKWING_CONFIG", path)
			services, err := resolveLocalServicesAt(t.Context(), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "admission") || services.runtime != nil {
				t.Fatalf("invalid admission constructed runtime: %v, %v", services.runtime, err)
			}
		})
	}
}

func TestLocalRuntimeConstructionRetainsAdmissionPolicyAndBudget(t *testing.T) {
	for _, mode := range []string{"off", "auto", "jev", "custom", "classic"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			body := "admission:\n  budget: 50%,ignore-external\n  mode: " + mode + "\n"
			if mode == "custom" || mode == "jev" {
				body += "  custom:\n    class_weight: {batch: 37}\n    aging_every: 5\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SPARKWING_CONFIG", path)
			home := t.TempDir()
			config, err := resolveLocalRuntimeConfig(home)
			if err != nil {
				t.Fatal(err)
			}
			got := config.Admission
			if got.Home != home || got.AdmissionPolicy == nil || string(got.AdmissionPolicy.Mode) != mode || got.Budget.CoresFraction != .5 || !got.Budget.IgnoreExternal || got.BudgetSource != wingd.BudgetSourceConfig || got.BudgetOrigin != path+" admission.budget" {
				t.Fatalf("lost admission configuration: %+v", got)
			}
			if (mode == "custom" || mode == "jev") && (got.AdmissionPolicy.Scheduling.ClassWeight["batch"] != 37 || got.AdmissionPolicy.Scheduling.AgingEvery != 5) {
				t.Fatalf("lost custom admission policy: %+v", got.AdmissionPolicy.Scheduling)
			}
		})
	}
}

func localServicesFixture(t testing.TB, home string) (wingd.LocalRuntimeConfig, crontimer.Host) {
	t.Helper()
	userHome := t.TempDir()
	return wingd.LocalRuntimeConfig{
			Admission: wingd.Config{Home: home},
			Open: func(context.Context, string) (wingd.LocalController, error) {
				t.Fatal("composition opened the controller before election")
				return nil, errors.New("unexpected controller open")
			},
		}, crontimer.Host{
			GOOS: runtime.GOOS, Home: userHome,
			ConfigHome: filepath.Join(userHome, ".config"), SparkwingHome: home,
			Binary: filepath.Join(userHome, "sparkwing"), Version: "test",
		}
}

func TestLocalServicesComposeRuntimeUnitsAndPrivateLogs(t *testing.T) {
	home := t.TempDir()
	config, host := localServicesFixture(t, home)
	services, err := composeLocalServices(t.Context(), config, host)
	if err != nil {
		t.Fatal(err)
	}
	if services.runtime == nil || services.lifecycle == nil || services.units == nil || services.options.Home != home || services.options.Executable != host.Binary || services.options.Version != host.Version {
		t.Fatalf("incomplete local services: %+v", services.options)
	}
	status, err := services.runtime.Status(t.Context())
	if err != nil || status.Running || status.PID != 0 {
		t.Fatalf("composition started an owner: %+v %v", status, err)
	}
	log, err := wingd.LogPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("owned controller log"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := services.openLog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err := errors.Join(err, file.Close()); err != nil || string(data) != "owned controller log" {
		t.Fatalf("log = %q, error = %v", data, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if file, err := services.openLog(ctx); file != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled log open = %v, %v", file, err)
	}
}

func TestProductionLocalControllerReconcilesAndServesSharedHandlers(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	owner, err := openLocalController(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := owner.Ready(); err == nil {
		t.Fatal("production controller was ready before reconciliation")
	}
	activity, err := owner.Reconcile(t.Context())
	if err != nil || activity != (wingd.LocalActivity{}) {
		t.Fatalf("empty-home reconciliation: %+v: %v", activity, err)
	}
	if err := owner.Ready(); err != nil {
		t.Fatal(err)
	}
	if key, err := owner.TypeSafeAPIKey(t.Context()); err != nil || key != "" {
		t.Fatalf("fresh home Jev key: configured=%t: %v", key != "", err)
	}
	for _, handler := range []struct {
		name string
		path string
	}{
		{"API", "/api/v1/auth/whoami"},
		{"dashboard", "/sparkwing-runtime.js"},
		{"shell", "/sparkwing-runtime.js"},
	} {
		response := httptest.NewRecorder()
		h := owner.HTTP().API
		want := 401
		switch handler.name {
		case "dashboard":
			h, want = owner.HTTP().Dashboard, 200
		case "shell":
			h, want = owner.HTTP().Shell, 200
		}
		h.ServeHTTP(response, httptest.NewRequest("GET", handler.path, nil))
		if response.Code != want {
			t.Fatalf("%s: HTTP %d: %s", handler.name, response.Code, response.Body)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Ready(); err == nil {
		t.Fatal("closed production controller remained ready")
	}
}

func TestProductionLocalSchedulesShareCapturedControllerHome(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	services, err := resolveLocalScheduleServices(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLocalSchedules(services); err != nil {
		t.Fatal(err)
	}
	if services.options.Home != home || services.units == nil || services.installTimer == nil || services.uninstallTimer == nil {
		t.Fatal("local schedules lost controller or native timer bindings")
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("schedule factory opened a writing store: %v", err)
	}
	foreign := services.options
	foreign.Home = t.TempDir()
	if err := services.installTimer(t.Context(), foreign); err == nil {
		t.Fatal("native timer accepted a different controller home")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := services.installTimer(ctx, services.options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled timer install: %v", err)
	}
	if err := services.uninstallTimer(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled timer removal: %v", err)
	}
}

func TestProductionLocalQueueAdvertisesUsableCapacity(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	var runner runners.StoreRunnerConfig
	owner, err := (localcontroller.Config{Bind: func(ctx context.Context, binding localcontroller.Binding) (localcontroller.Services, error) {
		services, err := bindLocalController(ctx, binding)
		if err == nil {
			policy, ok := binding.Controller.RunnerPolicy.(*controller.SignedRunnerAdmission)
			if !ok || policy.Resolver == nil || policy.Resolver == binding.Controller.VersionPolicy.Resolver || binding.Controller.LocalRunnerPolicy != binding.Controller.VersionPolicy {
				t.Fatal("local and controller runner policy bindings are incomplete")
			}
		}
		runner = services.Runner
		return services, err
	}}).Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	if runner.RequestCapacity == nil {
		t.Fatal("local queue cannot supply available CPU to matching")
	}
	capacity, available, err := runner.RequestCapacity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if verdict := match.Fits(capacity, available, match.Resources{Cores: .25}); !verdict.OK() {
		t.Fatalf("local queue refuses a fractional CPU job on this machine: %v", verdict)
	}
}

func TestProductionLocalQueueChecksVersionWithoutNetwork(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	home := t.TempDir()
	requests := 0
	transport := &http.Client{Transport: queueHostTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("network denied")
	})}
	var queue *runners.Loop
	owner, err := (localcontroller.Config{Bind: func(ctx context.Context, binding localcontroller.Binding) (localcontroller.Services, error) {
		services, err := bindLocalController(ctx, binding)
		if err != nil {
			return services, err
		}
		strict, err := engine.NewResolver(engine.Config{BuildVersion: "v1.2.3", CacheDirectory: filepath.Join(home, "engines"), HTTPClient: transport})
		if err != nil {
			return services, err
		}
		local, err := engine.NewResolver(engine.Config{BuildVersion: "v1.2.3", CacheDirectory: filepath.Join(home, "engines"), HTTPClient: transport, LocalOnly: true})
		if err != nil {
			return services, err
		}
		binding.Controller.RunnerPolicy = &controller.SignedRunnerAdmission{Resolver: strict}
		binding.Controller.LocalRunnerPolicy = &controller.SignedEngineVersionPolicy{Resolver: local}
		queue, err = runners.NewStoreRunner(binding.Controller, services.Runner)
		return services, err
	}}).Open(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := queue.CheckVersion(t.Context(), "v1.2.3"); err != nil {
		t.Fatalf("local queue version check: %v", err)
	}
	response, err := queue.Controller.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}, AvailableCores: float64(runtime.NumCPU())})
	if err != nil || response.Claim != nil {
		t.Fatalf("empty local queue: %+v: %v", response, err)
	}
	if requests != 0 {
		t.Fatalf("local queue made %d network requests", requests)
	}
}

func FuzzLocalServicesKeepOneCapturedHome(f *testing.F) {
	f.Add("controller", "controller")
	f.Add("controller", "other")
	f.Add("../controller", "\x00other")
	f.Fuzz(func(t *testing.T, runtimeName, unitName string) {
		root := t.TempDir()
		home := func(name string) string {
			hash := sha256.Sum256([]byte(name))
			return filepath.Join(root, hex.EncodeToString(hash[:]))
		}
		config, host := localServicesFixture(t, home(runtimeName))
		host.SparkwingHome = home(unitName)
		services, err := composeLocalServices(t.Context(), config, host)
		if runtimeName != unitName {
			if err == nil || services.runtime != nil || services.lifecycle != nil {
				t.Fatal("composed runtime and units across homes")
			}
			return
		}
		if err != nil || services.options.Home != host.SparkwingHome {
			t.Fatalf("lost captured home: %q %v", services.options.Home, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := services.runtime.Run(ctx, wingd.LocalStartOptions{Home: home(unitName + "other"), Version: "test"}); err == nil {
			t.Fatal("bound runtime accepted a different start home")
		}
	})
}

func TestStandaloneLocalQueueLeavesTheForegroundTriggerToItsCLI(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	var runner runners.StoreRunnerConfig
	owner, err := (localcontroller.Config{Bind: func(ctx context.Context, binding localcontroller.Binding) (localcontroller.Services, error) {
		services, err := bindStandaloneController(ctx, binding)
		runner = services.Runner
		return services, err
	}}).Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	if len(runner.Loop.Kinds) != 1 || runner.Loop.Kinds[0] != capability.UnitNode {
		t.Fatalf("standalone queue claims %v; the foreground CLI must claim its own trigger", runner.Loop.Kinds)
	}
}
