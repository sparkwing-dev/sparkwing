package gotoolchain_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
)

func fixture(t *testing.T, setting, current, floor string, environment bool) (string, []string, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/pipeline\n\ngo "+floor+"\ntoolchain go1.99.0\n", 0o600)
	goenv := filepath.Join(dir, "go-env")
	write(t, goenv, "GOTOOLCHAIN="+setting+"\n", 0o600)
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"if [ -f go.mod ]; then exit 42; fi\n" +
		"if [ \"$GOWORK\" != off ]; then exit 43; fi\n" +
		"printf 'probe\\n' >> '" + calls + "'\n" +
		"printf 'go: downloading selected toolchain\\n' >&2\n" +
		"printf '%s\\n' '" + setting + "' '" + current + "' '" + goenv + "'\n"
	write(t, filepath.Join(dir, "go"), script, 0o700)
	env := []string{"PATH=" + dir, "GOWORK=" + filepath.Join(dir, "go.work"), "USER_PIN=unchanged"}
	if environment {
		env = append(env, "GOTOOLCHAIN="+setting)
	}
	return dir, env, calls
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func value(env []string, key string) string {
	for _, item := range env {
		if v, ok := strings.CutPrefix(item, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestBuildEnv(t *testing.T) {
	cases := []struct{ name, setting, current, floor, override string }{
		{"fixed pin", "go1.26.6", "go1.26.6", "1.26.8", "go1.26.8"},
		{"no patch", "go1.25.9", "go1.25.9", "1.26", "go1.26.0"},
		{"newer", "go1.27.1", "go1.27.1", "1.26.8", "go1.27.1"},
		{"equal", "go1.26.8", "go1.26.8", "1.26.8", "go1.26.8"},
		{"auto", "auto", "go1.26.6", "1.26.8", "auto"},
		{"path", "path", "go1.26.6", "1.26.8", "path"},
		{"pin auto", "go1.26.6+auto", "go1.26.6", "1.26.8", "go1.26.6+auto"},
		{"pin path", "go1.26.6+path", "go1.26.6", "1.26.8", "go1.26.6+path"},
		{"local sufficient", "local", "go1.26.8", "1.26.8", "local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, env, calls := fixture(t, tc.setting, tc.current, tc.floor, true)
			var notices []string
			ctx := gotoolchain.WithSession(context.Background(), env, func(s string) { notices = append(notices, s) })
			for range 2 {
				build, err := gotoolchain.BuildEnv(ctx, dir, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				if got := value(build, "GOTOOLCHAIN"); got != tc.override {
					t.Errorf("build toolchain = %s, want %s", got, tc.override)
				}
				if value(build, "USER_PIN") != "unchanged" {
					t.Fatal("lost runtime variable")
				}
			}
			if value(env, "GOTOOLCHAIN") != tc.setting {
				t.Fatal("input runtime environment changed")
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(data), "probe") != 1 {
				t.Fatal("probed more than once")
			}
			wantNotices := 0
			if tc.override != tc.setting {
				wantNotices = 1
			}
			if len(notices) != wantNotices {
				t.Errorf("notices = %v", notices)
			}
		})
	}
}

func TestOverlayFloor(t *testing.T) {
	for _, overlayFloor := range []string{"1.26.9", "1.26.1"} {
		t.Run(overlayFloor, func(t *testing.T) {
			dir, env, _ := fixture(t, "go1.26.6", "go1.26.6", "1.26.8", true)
			write(t, filepath.Join(dir, ".resolved.mod"), "module example.com/pipeline\ngo "+overlayFloor+"\n", 0o600)
			report, err := gotoolchain.Inspect(context.Background(), dir, env, ".resolved.mod")
			if err != nil {
				t.Fatal(err)
			}
			want := "1.26.8"
			if overlayFloor == "1.26.9" {
				want = overlayFloor
			}
			if report.Floor != want || report.Override != "go"+want {
				t.Fatalf("report: %+v", report)
			}
		})
	}
}

func TestLocalBlockedSources(t *testing.T) {
	for _, environment := range []bool{true, false} {
		t.Run(map[bool]string{true: "environment", false: "go env file"}[environment], func(t *testing.T) {
			dir, env, _ := fixture(t, "local", "go1.26.6", "1.26.8", environment)
			report, err := gotoolchain.Inspect(context.Background(), dir, env, "")
			var blocked *gotoolchain.Error
			if !errors.As(err, &blocked) {
				t.Fatalf("error = %v", err)
			}
			wantSource := "the GOTOOLCHAIN environment variable"
			if !environment {
				wantSource = filepath.Join(dir, "go-env")
			}
			if report.Source != wantSource {
				t.Fatalf("source = %s", report.Source)
			}
			fix := "unset GOTOOLCHAIN"
			if !environment {
				fix = "go env -u GOTOOLCHAIN"
			}
			for _, fragment := range []string{"1.26.8", "1.26.6", "GOTOOLCHAIN=local", wantSource, fix} {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("missing %q: %v", fragment, err)
				}
			}
		})
	}
}

func TestExplainDependencyMismatch(t *testing.T) {
	dir, env, _ := fixture(t, "local", "go1.26.6", "1.26.0", false)
	ctx := gotoolchain.WithSession(context.Background(), env, func(string) {})
	if err := gotoolchain.ExplainOutput(ctx, "unrelated error", env); err != nil {
		t.Fatal(err)
	}
	err := gotoolchain.ExplainOutput(ctx, "go: example.com/dependency requires go >= 1.27.2 (running go 1.26.6; GOTOOLCHAIN=local)", env)
	var blocked *gotoolchain.Error
	if !errors.As(err, &blocked) {
		t.Fatalf("error = %v", err)
	}
	if blocked.Required != "1.27.2" || blocked.Current != "go1.26.6" || blocked.Source != filepath.Join(dir, "go-env") {
		t.Fatalf("error: %+v", blocked)
	}
}

func TestInvalidModule(t *testing.T) {
	dir, env, _ := fixture(t, "local", "go1.26.6", "1.26.0", true)
	write(t, filepath.Join(dir, "go.mod"), "module example.com/pipeline\ngo invalid\n", 0o600)
	if _, err := gotoolchain.BuildEnv(context.Background(), dir, env, ""); err == nil {
		t.Fatal("accepted invalid go directive")
	}
}

func TestCanceledProbe(t *testing.T) {
	dir, env, _ := fixture(t, "local", "go1.26.6", "1.26.0", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = gotoolchain.WithSession(ctx, env, func(string) {})
	if _, err := gotoolchain.BuildEnv(ctx, dir, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe error = %v", err)
	}
}

func TestFailedProbeLeavesEnvUnchanged(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/pipeline\n\ngo 1.26.8\n", 0o600)
	bin := t.TempDir()
	write(t, filepath.Join(bin, "go"), "#!/bin/sh\nexit 3\n", 0o700)
	env, err := gotoolchain.BuildEnv(context.Background(), dir, []string{"PATH=" + bin, "GOTOOLCHAIN=go1.26.6"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := value(env, "GOTOOLCHAIN"); got != "go1.26.6" {
		t.Fatalf("GOTOOLCHAIN = %q", got)
	}
}
