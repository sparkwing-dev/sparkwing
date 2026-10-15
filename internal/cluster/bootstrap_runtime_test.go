package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func bootstrapPlanFile(t testing.TB) (BootstrapOptions, launcher.CloudJobAward) {
	t.Helper()
	expires := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	award := launcher.CloudJobAward{Binding: launcher.CloudJobBinding{Kind: launcher.CloudPlan, UnitID: "plan", RunID: "run", Team: "team", Holder: "holder", Generation: 7, ParentCapabilityExpiry: expires}, Bootstrap: []byte("private-bootstrap"), CPUCores: 1, Memory: 1 << 30}
	directory := t.TempDir()
	body, err := json.Marshal(award)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"award.json": body, "bootstrap": award.Bootstrap} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return BootstrapOptions{ControllerURL: "https://controller.example", RunID: "run", Generation: 7, BootstrapFile: filepath.Join(directory, "bootstrap"), SourceDir: filepath.Join(directory, "source"), ModuleCache: filepath.Join(directory, "modules")}, award
}

func bootstrapArguments(options BootstrapOptions) []string {
	args := []string{"--controller=" + options.ControllerURL, "--run=" + options.RunID, "--generation=7", "--bootstrap-file=" + options.BootstrapFile, "--source-dir=" + options.SourceDir, "--module-cache=" + options.ModuleCache}
	if options.JobID != "" {
		args = append(args, "--job="+options.JobID)
	}
	return args
}

type runtimeExchange struct {
	controllerwire.Controller
	err error
}

func (r runtimeExchange) Exchange(context.Context) (controllerwire.ExchangeResponse, error) {
	return controllerwire.ExchangeResponse{}, r.err
}

func TestBootstrapRuntimePlanReachesExchangeWithoutJob(t *testing.T) {
	options, _ := bootstrapPlanFile(t)
	failure := errors.New("exchange boundary")
	closed := false
	runtime := BootstrapRuntime{Host: func(_ context.Context, got BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
		if got != options {
			t.Fatal("bootstrap options changed")
		}
		return launcher.BootstrapHost{Host: runtimeUnitHost{}, NewController: func(input client.EngineOptions) (controllerwire.Controller, error) {
			bearer, err := input.Bearer(t.Context())
			if err != nil || bearer != "private-bootstrap" || input.URL != options.ControllerURL {
				t.Fatal("bootstrap authority changed")
			}
			return runtimeExchange{err: failure}, nil
		}}, func() error { closed = true; return nil }, nil
	}}
	if err := runtime.Run(t.Context(), "run-node", bootstrapArguments(options)); !errors.Is(err, failure) || !closed {
		t.Fatalf("plan bootstrap: closed=%t error=%v", closed, err)
	}
}

type runtimeUnitHost struct{}

func (runtimeUnitHost) Host(context.Context, controllerwire.ClaimedUnit) error {
	return errors.New("unexpected execution")
}

func FuzzBootstrapRuntimeFence(f *testing.F) {
	f.Add("run", "", int64(7))
	f.Add("other", "job", int64(8))
	f.Fuzz(func(t *testing.T, run, job string, generation int64) {
		options, _ := bootstrapPlanFile(t)
		called := false
		runtime := BootstrapRuntime{Host: func(context.Context, BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
			called = true
			return launcher.BootstrapHost{}, nil, errors.New("host boundary")
		}}
		options.RunID, options.JobID, options.Generation = run, job, generation
		err := runtime.run(t.Context(), "run-node", options)
		if run != "run" || job != "" || generation != 7 {
			if called || err == nil {
				t.Fatal("changed argv fence reached host construction")
			}
		} else if !called || err == nil || strings.Contains(err.Error(), "private-bootstrap") {
			t.Fatal("matching award did not preserve host failure")
		}
	})
}

func TestBootstrapRuntimeJoinsPartialHostBinding(t *testing.T) {
	options, _ := bootstrapPlanFile(t)
	failure, cleanup := errors.New("host bind"), errors.New("cleanup")
	runtime := BootstrapRuntime{Host: func(context.Context, BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
		return launcher.BootstrapHost{}, func() error { return cleanup }, failure
	}}
	if err := runtime.run(t.Context(), "run-node", options); !errors.Is(err, failure) || !errors.Is(err, cleanup) {
		t.Fatal(err)
	}
}

func TestBootstrapRuntimeFetchBindsParsedControllerWithoutExchange(t *testing.T) {
	options, award := bootstrapPlanFile(t)
	award.Source = source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: strings.Repeat("a", 40)}
	body, err := json.Marshal(award)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(options.BootstrapFile), "award.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("source-access boundary")
	runtime := BootstrapRuntime{
		Source: func(_ context.Context, got BootstrapOptions) (launcher.SourceInit, error) {
			if got != options {
				t.Fatal("source binding lost parsed paths or controller")
			}
			return launcher.SourceInit{Bind: func(_ context.Context, got launcher.CloudJobAward) (source.Options, func() error, error) {
				if string(got.Bootstrap) != string(award.Bootstrap) || !reflect.DeepEqual(got.Source, award.Source) {
					t.Fatal("source binding lost the private award")
				}
				return source.Options{}, nil, failure
			}}, nil
		},
		Host: func(context.Context, BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
			t.Fatal("fetch-source constructed an exchange host")
			return launcher.BootstrapHost{}, nil, nil
		},
	}
	if err := runtime.Run(t.Context(), "fetch-source", bootstrapArguments(options)); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestBootstrapFileCancellationPrecedesAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	failure := errors.New("host stopped")
	cancel(failure)
	directory := t.TempDir()
	options := BootstrapOptions{ControllerURL: "https://controller.example", RunID: "run", Generation: 7, BootstrapFile: filepath.Join(directory, "absent"), SourceDir: filepath.Join(directory, "source"), ModuleCache: filepath.Join(directory, "modules")}
	if err := (BootstrapRuntime{}).Run(ctx, "run-node", bootstrapArguments(options)); !errors.Is(err, failure) {
		t.Fatal("cancelled host attempted private file acquisition")
	}
}
