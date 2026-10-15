package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestCacheWarmPreparesLocalEntryAndReportsClearing(t *testing.T) {
	for _, language := range []string{"go", "typescript"} {
		var calls []string
		deps := cacheWarmDependencies{
			buildEnv: cacheWarmTestEnvironment,
			locate:   func() (string, error) { calls = append(calls, "locate"); return "/checkout/.sparkwing", nil },
			clear: func(context.Context) (bincache.PruneResult, error) {
				calls = append(calls, "clear")
				return bincache.PruneResult{ReclaimedEntries: 2, LogicalRemovedBytes: 42}, nil
			},
			prepare: func(_ context.Context, dir string, env []string, _ io.Writer) (cacheWarmResult, error) {
				calls = append(calls, "prepare")
				if !reflect.DeepEqual(env, []string{"PATH=/tools"}) {
					t.Fatalf("preparation received the wrong environment: %v", env)
				}
				if dir != "/checkout/.sparkwing" {
					t.Fatalf("wrong checkout: %s", dir)
				}
				return cacheWarmResult{Kind: "cache_warm", Language: language, Path: "/cache/pipeline", Status: "prepared"}, nil
			},
		}
		var out bytes.Buffer
		if err := runCacheWarmCommand(t.Context(), []string{"--clear-cache", "-o", "json"}, &out, deps); err != nil {
			t.Fatal(err)
		}
		var result cacheWarmResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"locate", "clear", "prepare"}) || result.Language != language || result.Path != "/cache/pipeline" || result.Cleared == nil || result.Cleared.ReclaimedEntries != 2 || result.Cleared.LogicalRemovedBytes != 42 {
			t.Fatalf("lost warm result: %v %+v", calls, result)
		}
	}
}

func TestCacheWarmPreflightPreventsSideEffects(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"-o", "yaml"}, {"unexpected"}, {"--profile", "prod"}} {
		var out bytes.Buffer
		err := runCacheWarmCommand(t.Context(), args, &out, cacheWarmDependencies{})
		if err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("%v: %v %s", args, err, &out)
		}
	}
	var out bytes.Buffer
	if err := runCacheWarmCommand(t.Context(), []string{"--help", "-o", "json"}, &out, cacheWarmDependencies{}); err != nil || out.Len() == 0 {
		t.Fatalf("help: %v %s", err, &out)
	}
}

func TestCacheWarmFailureEmitsNoSuccess(t *testing.T) {
	for _, failAt := range []string{"locate", "clear", "prepare"} {
		failure := errors.New("failed at " + failAt)
		var calls []string
		check := func(at string) error {
			calls = append(calls, at)
			if at == failAt {
				return failure
			}
			return nil
		}
		deps := cacheWarmDependencies{
			buildEnv: cacheWarmTestEnvironment,
			locate:   func() (string, error) { return "/checkout/.sparkwing", check("locate") },
			clear:    func(context.Context) (bincache.PruneResult, error) { return bincache.PruneResult{}, check("clear") },
			prepare: func(context.Context, string, []string, io.Writer) (cacheWarmResult, error) {
				return cacheWarmResult{}, check("prepare")
			},
		}
		var out bytes.Buffer
		err := runCacheWarmCommand(t.Context(), []string{"--clear-cache", "-o", "json"}, &out, deps)
		if !errors.Is(err, failure) || out.Len() != 0 || calls[len(calls)-1] != failAt {
			t.Fatalf("%s: calls=%v err=%v output=%s", failAt, calls, err, &out)
		}
	}
}

func TestCacheWarmWithoutClearingReturnsThePath(t *testing.T) {
	deps := cacheWarmDependencies{
		buildEnv: cacheWarmTestEnvironment,
		locate:   func() (string, error) { return "/checkout/.sparkwing", nil },
		prepare: func(context.Context, string, []string, io.Writer) (cacheWarmResult, error) {
			return cacheWarmResult{Path: "/cache/pipeline", Status: "cached"}, nil
		},
	}
	var out bytes.Buffer
	if err := runCacheWarmCommand(t.Context(), []string{"-o", "plain"}, &out, deps); err != nil || out.String() != "/cache/pipeline\n" {
		t.Fatalf("path: %v %s", err, &out)
	}
	if err := runCacheWarmCommand(t.Context(), []string{"-o", "json"}, doctorFailedWriter{}, deps); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer error: %v", err)
	}
}

func TestCacheWarmCanceledBeforePreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	err := runCacheWarmCommand(ctx, []string{"--clear-cache"}, &out, cacheWarmDependencies{})
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancellation: %v %s", err, &out)
	}
}

func cacheWarmTestEnvironment(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) {
	return []string{"PATH=/tools"}, nil
}

func TestCacheWarmUsesInstallEnvironmentBeforeClearing(t *testing.T) {
	failure := errors.New("environment rejected")
	deps := cacheWarmDependencies{
		environment: []string{"PATH=/tools", "SECRET=private"},
		locate:      func() (string, error) { return "/checkout/.sparkwing", nil },
		buildEnv: func(kind procenv.Kind, base procenv.Base, rt procenv.Runtime) ([]string, error) {
			if kind != procenv.Install || !base.Local || !reflect.DeepEqual(base.Captured, []string{"PATH=/tools", "SECRET=private"}) || !reflect.DeepEqual(rt, procenv.Runtime{}) {
				t.Fatalf("wrong process policy: %v %+v %+v", kind, base, rt)
			}
			return nil, failure
		},
	}
	var out bytes.Buffer
	if err := runCacheWarmCommand(t.Context(), []string{"--clear-cache", "-o", "json"}, &out, deps); !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("environment failure: %v %s", err, &out)
	}
}

func TestCacheWarmMasksInstallerOutputAndErrors(t *testing.T) {
	failure := errors.New("installer failed for p@ss")
	var out, diagnostics bytes.Buffer
	deps := cacheWarmDependencies{
		locate:      func() (string, error) { return "/checkout/.sparkwing", nil },
		environment: []string{"PATH=/tools", "HTTPS_PROXY=http://alice:p%40ss@proxy.test", "AWS_SECRET_ACCESS_KEY=private-credential"},
		buildEnv:    procenv.Build, diagnostics: &diagnostics,
		prepare: func(_ context.Context, _ string, env []string, output io.Writer) (cacheWarmResult, error) {
			joined := strings.Join(env, "\n")
			if !strings.Contains(joined, "HTTPS_PROXY=") || strings.Contains(joined, "AWS_SECRET_ACCESS_KEY") {
				t.Fatalf("installer received the wrong filtered environment: %q", env)
			}
			for _, chunk := range []string{"using ali", "ce:p", "@ss\n"} {
				if _, err := io.WriteString(output, chunk); err != nil {
					return cacheWarmResult{}, err
				}
			}
			return cacheWarmResult{}, failure
		},
	}
	err := runCacheWarmCommand(t.Context(), []string{"-o", "json"}, &out, deps)
	if !errors.Is(err, failure) || out.Len() != 0 || strings.Contains(err.Error(), "p@ss") || strings.Contains(diagnostics.String(), "alice") || strings.Contains(diagnostics.String(), "p@ss") {
		t.Fatalf("installer leaked diagnostics: err=%v stdout=%s stderr=%s", err, &out, &diagnostics)
	}
}

func TestCacheWarmFlushesDiagnosticsBeforeSuccess(t *testing.T) {
	deps := cacheWarmDependencies{
		locate:   func() (string, error) { return "/checkout/.sparkwing", nil },
		buildEnv: procenv.Build, diagnostics: doctorFailedWriter{},
		prepare: func(_ context.Context, _ string, _ []string, output io.Writer) (cacheWarmResult, error) {
			_, err := io.WriteString(output, "done")
			return cacheWarmResult{Path: "/cache/pipeline"}, err
		},
	}
	var out bytes.Buffer
	err := runCacheWarmCommand(t.Context(), []string{"-o", "json"}, &out, deps)
	if !errors.Is(err, io.ErrClosedPipe) || out.Len() != 0 {
		t.Fatalf("diagnostics failure reported success: err=%v out=%s", err, &out)
	}
}

func TestCacheWarmUsesSelectedEngineAndCapturedEnvironment(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			dir := t.TempDir()
			manifest := "go.mod"
			body := "module example.com/fixture\ngo 1.26\n"
			if sdk == engine.TypeScriptSDK {
				manifest, body = "package.json", `{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"1.0.0"}}`
			}
			if err := os.WriteFile(filepath.Join(dir, manifest), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			selected := engine.Resolved{Executable: filepath.Join(t.TempDir(), "engine")}
			environment := []string{"CAPTURED=value"}
			called := false
			result, err := warmLocalPipelineWith(t.Context(), dir, environment, io.Discard, func(ctx context.Context, got string, language engine.SDKKind) (engine.Resolved, error) {
				if got != dir || language != sdk || ctx.Err() != nil {
					t.Fatal("wrong engine selection inputs")
				}
				return selected, nil
			}, func(ctx context.Context, got engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
				called = true
				if ctx.Err() != nil || got.Executable != selected.Executable || request.Directory != filepath.Dir(dir) || request.PipelineDir != filepath.Base(dir) || request.SDK != sdk || !reflect.DeepEqual(request.Environment, environment) {
					t.Fatal("installation changed selected engine or captured input")
				}
				return engine.SDKCommand{Directory: dir, Command: []string{filepath.Join(dir, "pipeline")}}, nil
			})
			if err != nil || !called || result.Language != string(sdk) || result.Status != "prepared" {
				t.Fatalf("result=%+v called=%v err=%v", result, called, err)
			}
			if sdk == engine.TypeScriptSDK && result.Path != filepath.Join(dir, "node_modules") {
				t.Fatalf("TypeScript path=%q", result.Path)
			}
		})
	}
}
