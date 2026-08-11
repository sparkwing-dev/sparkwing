package orchestrator

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func TestBuildRunInvocation_LogPathFromLocalLogBackend(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	dir := localRunLogDir(localLogs{paths: p}, "run-1")
	if want := p.RunDir("run-1"); dir != want {
		t.Fatalf("localRunLogDir = %q, want %q", dir, want)
	}
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", dir)
	got, ok := inv["log_path"].(string)
	if !ok {
		t.Fatalf("log_path missing or not a string: %#v", inv["log_path"])
	}
	if got != p.RunDir("run-1") {
		t.Errorf("log_path = %q, want %q", got, p.RunDir("run-1"))
	}
	if !filepath.IsAbs(got) {
		t.Errorf("log_path = %q, want an absolute path", got)
	}
	// The envelope and node logs must live under the advertised
	// directory; that is the whole point of publishing it.
	if d := filepath.Dir(p.EnvelopeLog("run-1")); d != got {
		t.Errorf("envelope log lives in %q, log_path says %q", d, got)
	}
	if d := filepath.Dir(p.NodeLog("run-1", "build")); d != got {
		t.Errorf("node log lives in %q, log_path says %q", d, got)
	}
	if _, ok := inv["log_path"].(string); ok {
		if hints, hasHints := inv["hints"].(map[string]string); hasHints {
			if _, buried := hints["log_path"]; buried {
				t.Error("log_path must be a top-level invocation field, not a hint")
			}
		}
	}
}

func TestBuildRunInvocation_NoLogPathWithoutLocalLogs(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", "")
	if v, ok := inv["log_path"]; ok {
		t.Errorf("log_path must be omitted when the run writes no local logs; got %v", v)
	}
}

func TestLocalRunLogDir_NonLocalBackendsReportNothing(t *testing.T) {
	for name, b := range map[string]LogBackend{
		"log store": NewLogStoreBackend(storage.LogStore(nil), nil),
		"http logs": NewHTTPLogsWithToken("https://controller.example.dev", nil, "tok", nil),
		"nil":       nil,
	} {
		if dir := localRunLogDir(b, "run-1"); dir != "" {
			t.Errorf("%s backend: localRunLogDir = %q, want empty", name, dir)
		}
	}
}
