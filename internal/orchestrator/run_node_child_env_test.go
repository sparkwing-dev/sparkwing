//go:build unix

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func brokeredChildEnv(t *testing.T, runnerInfo *sparkwing.RunnerInfo) map[string]string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	script := filepath.Join(dir, "pipeline")
	body := "#!/bin/sh\nenv > '" + out + "'\ncat > /dev/null\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(upstream.Close)
	ctx := context.Background()
	if runnerInfo != nil {
		ctx = sparkwingruntime.WithRunner(ctx, runnerInfo)
	}
	res, err := runNodeChild(ctx, script, dir, upstream.URL, "", "parent-token", "", "",
		"run-1", "build/linux", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || res.Err != nil {
		t.Fatalf("brokered child = %+v, %v", res, err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			env[name] = value
		}
	}
	return env
}

func TestBrokeredChildGetsTheNodeIdentityAndLogFormat(t *testing.T) {
	env := brokeredChildEnv(t, nil)
	for name, want := range map[string]string{
		"SPARKWING_RUN_ID":     "run-1",
		"SPARKWING_NODE_ID":    "build/linux",
		"SPARKWING_LOG_FORMAT": "json",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

func TestBrokeredChildGetsItsRunnersIdentity(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "warm-pool-a")
	t.Setenv("SPARKWING_RUNNER_TYPE", "kubernetes")
	t.Setenv("SPARKWING_RUNNER_LABELS", "kubernetes,gpu")
	env := brokeredChildEnv(t, nil)
	if env["SPARKWING_RUNNER_NAME"] != "warm-pool-a" || env["SPARKWING_RUNNER_TYPE"] != "kubernetes" ||
		env["SPARKWING_RUNNER_LABELS"] != "kubernetes,gpu" {
		t.Fatalf("runner identity from the supervisor's environment = %v", env)
	}
	env = brokeredChildEnv(t, &sparkwing.RunnerInfo{Name: "runner:mac-mini", Labels: []string{"os=darwin", "xcode"}})
	if env["SPARKWING_RUNNER_NAME"] != "runner:mac-mini" || env["SPARKWING_RUNNER_LABELS"] != "os=darwin,xcode" {
		t.Fatalf("runner identity from the pool = %v", env)
	}
	if _, set := env["SPARKWING_RUNNER_TYPE"]; set {
		t.Fatalf("an unclassified runner named a type: %v", env)
	}
}
