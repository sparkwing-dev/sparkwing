package nodehost_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func mustRegisterCleanup(t *testing.T, cleanups *nodehost.ProcessCleanups, argv ...string) string {
	t.Helper()
	id, err := cleanups.Register(argv, "")
	if err != nil || id == "" {
		t.Fatalf("register %v: id=%q error=%v", argv, id, err)
	}
	return id
}

func cleanupContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the cleanups under test are POSIX shell commands")
	}
}

func shellEnv(t testing.TB, extra ...string) procenv.Env {
	t.Helper()
	return testProcessEnv(t, append([]string{"PATH=" + os.Getenv("PATH")}, extra...)...)
}

func TestCleanupsRunNewestFirstSkippingReleasedOnes(t *testing.T) {
	requireUnix(t)
	cleanups := &nodehost.ProcessCleanups{}
	dir := t.TempDir()
	for _, n := range []string{"first", "second", "third"} {
		id := mustRegisterCleanup(t, cleanups, "sh", "-c", "echo "+n+" >> order")
		if n == "second" {
			if err := cleanups.Release(id); err != nil {
				t.Fatal(err)
			}
			if err := cleanups.Release(id); !cleanupErrorCode(err, apierr.NotFound) {
				t.Fatalf("second release: error=%v, want not_found", err)
			}
		}
	}
	if err := cleanups.Run(cleanupContext(t), nodehost.CleanupScope{Dir: dir, Env: shellEnv(t)}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "order"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third\nfirst\n" {
		t.Fatalf("cleanups ran as %q, want third then first", got)
	}
	if err := cleanups.Run(cleanupContext(t), nodehost.CleanupScope{Dir: dir, Env: shellEnv(t)}); err != nil {
		t.Fatal(err)
	}
	if again, err := os.ReadFile(filepath.Join(dir, "order")); err != nil || !bytes.Equal(again, got) {
		t.Fatalf("a second cleanup run changed the output: %q error=%v", again, err)
	}
}

func TestCleanupRunsWithTheNodesEnvironmentNotTheHosts(t *testing.T) {
	requireUnix(t)
	t.Setenv("HOST_ONLY_CREDENTIAL", "host-secret")
	cleanups := &nodehost.ProcessCleanups{}
	mustRegisterCleanup(t, cleanups, "sh", "-c", `echo "node=$NODE_VAR host=$HOST_ONLY_CREDENTIAL token=$SPARKWING_NODE_TOKEN dir=$(pwd)"`)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	scope := nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv(t, "NODE_VAR=node-value")}
	if err := cleanups.Run(cleanupContext(t), scope); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("node=node-value host= token= dir=%s\n", dir)
	if !strings.Contains(log.String(), want) {
		t.Fatalf("cleanup output:\n%s\nwant a line %q", log.String(), want)
	}
}

func TestCleanupResolvesItsProgramOnTheNodesPath(t *testing.T) {
	requireUnix(t)
	cleanups := &nodehost.ProcessCleanups{}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "node-tool"), []byte("#!/bin/sh\necho node-tool ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRegisterCleanup(t, cleanups, "node-tool")
	var log bytes.Buffer
	scope := nodehost.CleanupScope{Dir: t.TempDir(), Log: &log, Env: testProcessEnv(t, "PATH="+bin)}
	if err := cleanups.Run(cleanupContext(t), scope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "node-tool ran") {
		t.Fatalf("cleanup output:\n%s\nwant node-tool found on the node's PATH", log.String())
	}
}

func TestCleanupFailuresNameOnlyTheProgramAndTheRestStillRun(t *testing.T) {
	requireUnix(t)
	cleanups := &nodehost.ProcessCleanups{}
	dir := t.TempDir()
	mustRegisterCleanup(t, cleanups, "sh", "-c", "touch ran")
	mustRegisterCleanup(t, cleanups, "no-such-program-xyz", "--token=s3cret-arg")
	mustRegisterCleanup(t, cleanups, "sh", "-c", "exit 3", "s3cret-arg")
	var log bytes.Buffer
	if err := cleanups.Run(cleanupContext(t), nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv(t)}); err == nil {
		t.Fatal("failed cleanups returned success")
	}
	out := log.String()
	for _, want := range []string{"cleanup sh failed: exit status 3", "cleanup no-such-program-xyz failed: not found"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cleanup log:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "s3cret-arg") {
		t.Fatalf("logs carry a cleanup argument:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err != nil {
		t.Fatalf("the oldest cleanup did not run after two failures: %v", err)
	}
}

func TestCleanupsAreConfinedToTheirNode(t *testing.T) {
	requireUnix(t)
	cleanupsA, cleanupsB := &nodehost.ProcessCleanups{}, &nodehost.ProcessCleanups{}
	dir := t.TempDir()
	id := mustRegisterCleanup(t, cleanupsA, "sh", "-c", "touch a-ran")
	if err := cleanupsB.Release(id); !cleanupErrorCode(err, apierr.NotFound) || !strings.Contains(err.Error(), "no cleanup with that ID") {
		t.Fatalf("node B released node A's cleanup: error=%v", err)
	}
	mustRegisterCleanup(t, cleanupsB, "sh", "-c", "touch b-ran")
	if err := cleanupsA.Run(cleanupContext(t), nodehost.CleanupScope{Dir: dir, Env: shellEnv(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a-ran")); err != nil {
		t.Fatalf("node A's cleanup did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node A's exit ran node B's cleanup: %v", err)
	}
}

func cleanupErrorCode(err error, code apierr.Code) bool {
	var apiError *apierr.Error
	return errors.As(err, &apiError) && apiError.Code == code
}

func TestCleanupRegistrationLimits(t *testing.T) {
	cleanups := &nodehost.ProcessCleanups{}
	for name, input := range map[string]struct {
		argv        []string
		description string
	}{
		"empty argv":       {},
		"empty program":    {argv: []string{""}},
		"NUL in argv":      {argv: []string{"rm", "a\x00b"}},
		"65 arguments":     {argv: append([]string{"x"}, make([]string, 64)...)},
		"long description": {argv: []string{"x"}, description: strings.Repeat("d", 257)},
	} {
		if _, err := cleanups.Register(input.argv, input.description); !cleanupErrorCode(err, apierr.BadRequest) {
			t.Fatalf("%s: error=%v, want bad_request", name, err)
		}
	}
	if _, err := cleanups.Register(append([]string{"x"}, make([]string, 63)...), strings.Repeat("d", 256)); err != nil {
		t.Fatalf("64 arguments and a 256-byte description: %v", err)
	}
	var first string
	for i := 1; i < 16; i++ {
		id := mustRegisterCleanup(t, cleanups, "true")
		if first == "" {
			first = id
		}
	}
	if _, err := cleanups.Register([]string{"true"}, ""); !cleanupErrorCode(err, apierr.BadRequest) {
		t.Fatalf("17th cleanup: error=%v, want bad_request", err)
	}
	if err := cleanups.Release(first); err != nil {
		t.Fatal(err)
	}
	mustRegisterCleanup(t, cleanups, "true")
}

func testProcessEnv(t testing.TB, values ...string) procenv.Env {
	t.Helper()
	env, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: values}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	return env
}
