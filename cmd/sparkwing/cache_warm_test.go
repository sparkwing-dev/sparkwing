package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestCacheWarmPreparesLocalEntryAndReportsClearing(t *testing.T) {
	for _, language := range []string{"go", "typescript"} {
		var calls []string
		deps := cacheWarmDependencies{buildEnv: cacheWarmTestEnvironment,
			locate: func() (string, error) { calls = append(calls, "locate"); return "/checkout/.sparkwing", nil },
			clear: func(context.Context) (bincache.PruneResult, error) {
				calls = append(calls, "clear")
				return bincache.PruneResult{ReclaimedEntries: 2, LogicalRemovedBytes: 42}, nil
			},
			prepare: func(_ context.Context, dir string, env []string) (cacheWarmResult, error) {
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
		deps := cacheWarmDependencies{buildEnv: cacheWarmTestEnvironment,
			locate: func() (string, error) { return "/checkout/.sparkwing", check("locate") },
			clear:  func(context.Context) (bincache.PruneResult, error) { return bincache.PruneResult{}, check("clear") },
			prepare: func(context.Context, string, []string) (cacheWarmResult, error) {
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
	deps := cacheWarmDependencies{buildEnv: cacheWarmTestEnvironment,
		locate: func() (string, error) { return "/checkout/.sparkwing", nil },
		prepare: func(context.Context, string, []string) (cacheWarmResult, error) {
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
