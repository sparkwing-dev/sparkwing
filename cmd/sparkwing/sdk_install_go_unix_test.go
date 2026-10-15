//go:build !windows

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func goInstallerFixture(t *testing.T, fail bool) (sdkInstaller, string, string) {
	t.Helper()
	installer, request := sdkInstallFixture(t, engine.GoSDK)
	var input engine.SDKInstallRequest
	if err := json.Unmarshal([]byte(request), &input); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(input.Directory, input.PipelineDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module example.com/pipeline\n\ngo 1.26\n", "main.go": "package main\nfunc main() {}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "builds")
	script := `#!/bin/sh
if [ "$1" = env ]; then
  if [ "$PROBE_FAIL" = 1 ]; then printf '%s\n' "$BUILD_DIAGNOSTIC" >&2; exit 1; fi
  printf 'auto\ngo1.26.6\noff\n'; exit 0
fi
if [ -n "$CONTROLLER_TOKEN" ]; then exit 71; fi
if [ "$1" = mod ]; then
  printf 'download\n' >> "$BUILD_LOG"
  printf 'download as alice and p@ss\n'
  : > "$SUM_READY"
  exit 0
fi
printf 'build\n' >> "$BUILD_LOG"
printf 'download as alice and p@ss\n' >&2
printf '%s\n' "$BUILD_DIAGNOSTIC" >&2
if [ "$SUM_RECOVERY" = 1 ] && [ ! -f "$SUM_READY" ]; then printf 'missing go.sum entry\n' >&2; exit 1; fi
if [ "$BUILD_FAIL" = 1 ]; then exit 9; fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then shift; printf 'pipeline' > "$1"; /bin/chmod +x "$1"; exit 0; fi
  shift
done
exit 72
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	installer.environment = []string{"PATH=" + bin, "GOWORK=off", "BUILD_LOG=" + log, "SUM_READY=" + filepath.Join(bin, "sum-ready"), "CONTROLLER_TOKEN=private-controller", "HTTPS_PROXY=http://alice:p%40ss@proxy.example"}
	if fail {
		installer.environment = append(installer.environment, "BUILD_FAIL=1")
	}
	installer.prepare = prepareInstalledSDK
	t.Setenv("SPARKWING_HOME", t.TempDir())
	return installer, request, log
}

func TestSDKInstallGoBuildsAndReusesLocalBinary(t *testing.T) {
	installer, request, log := goInstallerFixture(t, false)
	var paths []string
	for range 2 {
		var out, diagnostics bytes.Buffer
		if err := installer.run(t.Context(), nil, strings.NewReader(request), &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		var result engine.SDKCommand
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Command) != 1 || !filepath.IsAbs(result.Command[0]) {
			t.Fatalf("invalid Go command: %+v", result)
		}
		body, err := os.ReadFile(result.Command[0])
		if err != nil || string(body) != "pipeline" {
			t.Fatalf("Go binary: %q %v", body, err)
		}
		if strings.Contains(diagnostics.String(), "alice") || strings.Contains(diagnostics.String(), "p@ss") {
			t.Fatal("Go diagnostics exposed proxy credentials")
		}
		paths = append(paths, result.Command[0])
	}
	builds, err := os.ReadFile(log)
	if err != nil || string(builds) != "build\n" || paths[0] != paths[1] {
		t.Fatalf("cache reuse: paths=%v builds=%q error=%v", paths, builds, err)
	}
}

func TestSDKInstallGoWithNativeCompiler(t *testing.T) {
	installer, request, _ := goInstallerFixture(t, false)
	installer.environment = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	var out, diagnostics bytes.Buffer
	if err := installer.run(t.Context(), nil, strings.NewReader(request), &out, &diagnostics); err != nil {
		t.Fatalf("native Go build: %v\n%s", err, &diagnostics)
	}
	var result engine.SDKCommand
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(result.Command[0])
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Size() == 0 {
		t.Fatalf("native executable: %v %v", info, err)
	}
}

func TestSDKInstallGoFailureDoesNotPublishCacheEntry(t *testing.T) {
	installer, request, _ := goInstallerFixture(t, true)
	var out, diagnostics bytes.Buffer
	err := installer.run(t.Context(), nil, strings.NewReader(request), &out, &diagnostics)
	if err == nil || out.Len() != 0 || !strings.Contains(diagnostics.String(), "***") {
		t.Fatalf("failed build: error=%v output=%q diagnostics=%q", err, &out, &diagnostics)
	}
	installer.environment = installer.environment[:len(installer.environment)-1]
	if err := installer.run(t.Context(), nil, strings.NewReader(request), &out, io.Discard); err != nil {
		t.Fatalf("failed build poisoned later installation: %v", err)
	}
}

func TestSDKInstallGoMissingSumRecoveryKeepsInstallBoundary(t *testing.T) {
	installer, request, log := goInstallerFixture(t, false)
	installer.environment = append(installer.environment, "SUM_RECOVERY=1")
	var out, diagnostics bytes.Buffer
	if err := installer.run(t.Context(), nil, strings.NewReader(request), &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	builds, err := os.ReadFile(log)
	if err != nil || string(builds) != "build\ndownload\nbuild\n" || !json.Valid(out.Bytes()) || strings.Contains(diagnostics.String(), "alice") || strings.Contains(diagnostics.String(), "p@ss") {
		t.Fatalf("sum recovery: builds=%q output=%q diagnostics=%q error=%v", builds, &out, &diagnostics, err)
	}
}

func TestSDKInstallGoFailedProbeDoesNotLogPrivateDiagnostics(t *testing.T) {
	installer, request, _ := goInstallerFixture(t, false)
	installer.environment = append(installer.environment, "PROBE_FAIL=1", "BUILD_DIAGNOSTIC=alice:p@ss")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "alice") || strings.Contains(logs.String(), "p@ss") {
		t.Fatal("toolchain probe exposed private diagnostics through debug logging")
	}
}

func TestSDKInstallGoPreservesDiagnosticWriteFailure(t *testing.T) {
	installer, request, _ := goInstallerFixture(t, false)
	err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, doctorFailedWriter{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("compiler lost diagnostic write failure: %v", err)
	}
}

func FuzzSDKInstallGoNeverReceivesControllerCredential(f *testing.F) {
	f.Add("private-controller")
	f.Fuzz(func(t *testing.T, value string) {
		if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			t.Skip()
		}
		installer, request, _ := goInstallerFixture(t, false)
		installer.environment = append(installer.environment, "CONTROLLER_TOKEN=controller-private:"+value)
		if err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzSDKInstallGoMasksCompilerDiagnostics(f *testing.F) {
	f.Add("secret with spaces and /?quotes\"")
	f.Fuzz(func(t *testing.T, value string) {
		if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			t.Skip()
		}
		secret := "go-password:" + value
		forms := []string{secret, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret))}
		installer, request, _ := goInstallerFixture(t, false)
		proxy := (&url.URL{Scheme: "http", Host: "proxy.example", User: url.UserPassword("go-user", secret)}).String()
		installer.environment = append(installer.environment, "HTTPS_PROXY="+proxy, "BUILD_DIAGNOSTIC="+strings.Join(forms, "\n"))
		var diagnostics, logs bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		t.Cleanup(func() { slog.SetDefault(previous) })
		if len(value)%2 == 0 {
			installer.environment = append(installer.environment, "PROBE_FAIL=1")
		}
		if err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, &diagnostics); err != nil {
			t.Fatal(err)
		}
		for _, form := range forms {
			if strings.Contains(diagnostics.String(), form) || strings.Contains(logs.String(), form) {
				t.Fatal("compiler diagnostic bypassed installation masking")
			}
		}
	})
}
