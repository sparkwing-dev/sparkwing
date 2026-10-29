package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestCacheExplainJSONEmitsReport(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCacheExplainArgs([]string{"-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache explain: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["dir"] != dir || payload["key"] == "" {
		t.Fatalf("explain payload = %#v", payload)
	}
}

func TestCacheExplainSaysATypeScriptPipelineIsNotCached(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"0.1.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCacheExplainArgs([]string{"-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache explain: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["dir"] != dir || payload["cached"] != false || payload["reason"] == nil {
		t.Fatalf("explain payload = %#v", payload)
	}
}

func TestCacheExplainJSONReportsParseFailureBeforeOutputFlag(t *testing.T) {
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCacheExplainArgs([]string{"--unknown", "-ojson"})
	})
	if runErr == nil {
		t.Fatal("cache explain hid parse failure")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func seedCommandCacheEntry(t *testing.T, body string) *bincache.Lease {
	t.Helper()
	entry, err := bincache.PipelineEntry("11111111-11111111")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte(body), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func decodeCachePayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode cache report: %v", err)
	}
	if _, ok := payload["payload"]; ok {
		t.Fatal("report wrapped in envelope")
	}
	return payload
}

func TestCacheInfoJSONReportsManagedBytes(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedCommandCacheEntry(t, "active")
	defer func() { _ = lease.Release() }()
	var runErr error
	out := captureStdout(t, func() { runErr = runCacheInfoArgs([]string{"-o", "json"}) })
	if runErr != nil {
		t.Fatalf("cache info: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["total_bytes"] != float64(6+65) || payload["entries"] != float64(1) {
		t.Fatalf("status payload = %#v", payload)
	}
}

func TestCachePruneJSONReportsBoundedOutcome(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedCommandCacheEntry(t, "remove")
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePruneArgs([]string{"--all-entries", "-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache prune: %v", runErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["reclaimed_entries"] != float64(1) || payload["goal_satisfied"] != true {
		t.Fatalf("prune payload = %#v", payload)
	}
}

func TestCachePruneRejectsInvalidLimit(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePruneArgs([]string{"--max-bytes", "invalid", "-o", "json"})
	})
	if runErr == nil {
		t.Fatal("cache prune accepted an invalid byte ceiling")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func TestCachePruneJSONReportsAPIFailure(t *testing.T) {
	original := pruneCacheToLimits
	t.Cleanup(func() { pruneCacheToLimits = original })
	pruneCacheToLimits = func(context.Context, int64, int, bool) (bincache.PruneResult, error) {
		return bincache.PruneResult{}, errors.New("store unavailable")
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePruneArgs([]string{"--all-entries", "-o", "json"})
	})
	if runErr == nil {
		t.Fatal("cache prune hid API failure")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func TestCachePruneJSONReportsParseFailure(t *testing.T) {
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCachePruneArgs([]string{"-o", "json", "--unknown"})
	})
	if runErr == nil {
		t.Fatal("cache prune hid parse failure")
	}
	if out != "" {
		t.Fatalf("failure polluted stdout: %q", out)
	}
}

func TestCachePruneJSONReportsParseFailureBeforeOutputFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown", "-o", "json"},
		{"--unknown", "-ojson"},
		{"--unknown", "-o=json"},
		{"--unknown", "--output", "json"},
		{"--unknown", "--output=json"},
	} {
		var runErr error
		out := captureStdout(t, func() {
			runErr = runCachePruneArgs(args)
		})
		if runErr == nil {
			t.Fatalf("cache prune hid parse failure for %q", args)
		}
		if out != "" {
			t.Fatalf("failure polluted stdout: %q", out)
		}
	}
}

func TestCachePruneRejectsOutputBeforeMutation(t *testing.T) {
	original := pruneCacheToLimits
	t.Cleanup(func() { pruneCacheToLimits = original })
	calls := 0
	pruneCacheToLimits = func(context.Context, int64, int, bool) (bincache.PruneResult, error) {
		calls++
		return bincache.PruneResult{}, nil
	}
	if err := runCachePruneArgs([]string{"--all-entries", "-o", "bogus"}); err == nil {
		t.Fatal("cache prune accepted an invalid output format")
	}
	if calls != 0 {
		t.Fatalf("prune calls = %d, want 0 before output validation", calls)
	}
}

func TestCacheExplainFindsTheModuleFromASubdirectory(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	sub := filepath.Join(root, "services", "api")
	for _, d := range []string{dir, sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCacheExplainArgs([]string{"-o", "json"})
	})
	if runErr != nil {
		t.Fatalf("cache explain from a subdirectory: %v", runErr)
	}
	if payload := decodeCachePayload(t, out); payload["dir"] != dir {
		t.Fatalf("explain dir = %v, want the module above the working directory %s", payload["dir"], dir)
	}
}

func TestCacheExplainFindsInstalledSDKBinary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(root, "missing.yaml"))
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOOS", "js")
	t.Setenv("GOARCH", "wasm")
	dir := filepath.Join(root, ".sparkwing")
	sdk := filepath.Join(root, "sdk")
	for _, path := range []string{dir, sdk} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	inputs := map[string]string{
		filepath.Join(dir, "go.mod"):  "module fixture\ngo 1.26\nrequire " + engine.GoModule + " v0.20.0\nreplace " + engine.GoModule + " => " + filepath.ToSlash(sdk) + "\n",
		filepath.Join(sdk, "go.mod"):  "module " + engine.GoModule + "\ngo 1.26\n",
		filepath.Join(dir, "main.go"): "package main\nfunc main() {}\n",
	}
	for path, body := range inputs {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.GoSDK, ExecutableDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := sdkinstall.Run(t.Context(), "development", bytes.NewReader(request), &output, &diagnostics); err != nil {
		t.Fatalf("install: %v: %s", err, &diagnostics)
	}
	var installed engine.SDKCommand
	if err := json.Unmarshal(output.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	var explainErr error
	out := captureStdout(t, func() { explainErr = runCacheExplainArgs([]string{"-o", "json"}) })
	if explainErr != nil {
		t.Fatal(explainErr)
	}
	payload := decodeCachePayload(t, out)
	if payload["cached"] != true {
		t.Fatalf("installed SDK binary is not cached: %s", out)
	}
	entry, err := bincache.PipelineEntry(payload["key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	lease, found, err := entry.Acquire(t.Context())
	if err != nil || !found {
		t.Fatalf("explained cache entry: %t %v", found, err)
	}
	defer func() { _ = lease.Release() }()
	if lease.CachedPath() != installed.CachedExecutable {
		t.Fatalf("explained cache entry %s differs from installed %s", lease.CachedPath(), installed.CachedExecutable)
	}
}
