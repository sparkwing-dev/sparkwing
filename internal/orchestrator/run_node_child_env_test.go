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
)

func brokeredChildEnv(t *testing.T) map[string]string {
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
	res, err := runNodeChild(context.Background(), script, dir, upstream.URL, "", "parent-token", "", "",
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
	env := brokeredChildEnv(t)
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
