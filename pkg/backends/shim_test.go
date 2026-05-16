package backends

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestLegacyURLToSpec(t *testing.T) {
	cases := []struct {
		in   string
		want *Spec
	}{
		{"fs:///tmp/sw-logs", &Spec{Type: TypeFilesystem, Path: "/tmp/sw-logs"}},
		{"s3://my-bucket", &Spec{Type: TypeS3, Bucket: "my-bucket"}},
		{"s3://my-bucket/prefix/path", &Spec{Type: TypeS3, Bucket: "my-bucket", Prefix: "prefix/path"}},
		{"s3://my-bucket/prefix/path/", &Spec{Type: TypeS3, Bucket: "my-bucket", Prefix: "prefix/path"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := LegacyURLToSpec(tc.in)
			if !ok {
				t.Fatalf("ok=false")
			}
			if got.Type != tc.want.Type || got.Path != tc.want.Path || got.Bucket != tc.want.Bucket || got.Prefix != tc.want.Prefix {
				t.Errorf("got %+v want %+v", got, tc.want)
			}
		})
	}
	bad := []string{"", "no-scheme", "fs://relative", "fs://", "s3://"}
	for _, in := range bad {
		t.Run("bad/"+in, func(t *testing.T) {
			if _, ok := LegacyURLToSpec(in); ok {
				t.Errorf("expected !ok for %q", in)
			}
		})
	}
}

func TestApplyLegacyEnvShim_PopulatesAndWarnsOnce(t *testing.T) {
	resetShimWarnedForTest()
	t.Setenv("SPARKWING_LOG_STORE", "fs:///tmp/sw-logs-test")
	t.Setenv("SPARKWING_ARTIFACT_STORE", "s3://team-cache/prefix")

	stderr, restore := captureStderr(t)
	defer restore()

	first := ApplyLegacyEnvShim(File{})
	if first.Defaults.Logs == nil || first.Defaults.Logs.Path != "/tmp/sw-logs-test" {
		t.Errorf("logs = %+v", first.Defaults.Logs)
	}
	if first.Defaults.Cache == nil || first.Defaults.Cache.Bucket != "team-cache" {
		t.Errorf("cache = %+v", first.Defaults.Cache)
	}

	// Second call: no warning, same population.
	second := ApplyLegacyEnvShim(File{})
	if second.Defaults.Logs == nil {
		t.Errorf("logs missing on second call")
	}

	got := stderr()
	if !strings.Contains(got, "deprecated") || !strings.Contains(got, "backends.yaml") {
		t.Errorf("missing deprecation warning, got: %q", got)
	}
	if strings.Count(got, "deprecated") != 1 {
		t.Errorf("expected warning once, saw %d: %q", strings.Count(got, "deprecated"), got)
	}
}

func TestApplyLegacyEnvShim_LosesToExplicitDefaults(t *testing.T) {
	resetShimWarnedForTest()
	t.Setenv("SPARKWING_LOG_STORE", "fs:///tmp/shim-logs")
	t.Setenv("SPARKWING_ARTIFACT_STORE", "s3://shim-bucket")

	_, restore := captureStderr(t)
	defer restore()

	// File declares an explicit Logs default; shim cache fills in
	// the missing surface.
	in := File{Defaults: Surfaces{
		Logs: &Spec{Type: TypeFilesystem, Path: "/explicit/logs"},
	}}
	out := ApplyLegacyEnvShim(in)
	if out.Defaults.Logs.Path != "/explicit/logs" {
		t.Errorf("explicit logs lost, got %+v", out.Defaults.Logs)
	}
	if out.Defaults.Cache == nil || out.Defaults.Cache.Bucket != "shim-bucket" {
		t.Errorf("shim cache not applied, got %+v", out.Defaults.Cache)
	}
}

func TestApplyLegacyEnvShim_NoVarsNoWarning(t *testing.T) {
	resetShimWarnedForTest()
	os.Unsetenv("SPARKWING_LOG_STORE")
	os.Unsetenv("SPARKWING_ARTIFACT_STORE")

	stderr, restore := captureStderr(t)
	defer restore()

	ApplyLegacyEnvShim(File{})
	if got := stderr(); got != "" {
		t.Errorf("unexpected stderr: %q", got)
	}
}

func TestBuiltinEnvironments_DetectRules(t *testing.T) {
	b := BuiltinEnvironments()
	gha := b.Environments["gha"]
	if gha.Detect.EnvVar != "GITHUB_ACTIONS" || gha.Detect.Equals != "true" {
		t.Errorf("gha detect = %+v", gha.Detect)
	}
	k8s := b.Environments["kubernetes"]
	if k8s.Detect.EnvVar != "KUBERNETES_SERVICE_HOST" || !k8s.Detect.Present {
		t.Errorf("k8s detect = %+v", k8s.Detect)
	}
	if k8s.Cache.Type != TypeController || k8s.Logs.Type != TypeController {
		t.Errorf("k8s surfaces = %+v", k8s.Surfaces)
	}
}

func captureStderr(t *testing.T) (read func() string, restore func()) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	read = func() string {
		_ = w.Close()
		os.Stderr = orig
		return string(<-done)
	}
	restore = func() {
		if os.Stderr != orig {
			_ = w.Close()
			os.Stderr = orig
			<-done
		}
	}
	return read, restore
}
