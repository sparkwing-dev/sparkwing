package backends_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoad_RoundTrip_AllSurfaces(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "backends.yaml", `
defaults:
  cache:
    type: filesystem
    path: /var/cache/sparkwing
    binaries:
      type: s3
      bucket: sparkwing-binaries
      prefix: bin/
  logs:
    type: filesystem
    path: /var/log/sparkwing
  state:
    type: sqlite
    path: /var/lib/sparkwing/state.db

environments:
  gha:
    detect:
      env_var: GITHUB_ACTIONS
      equals: "true"
    cache: { type: s3, bucket: gha-cache, prefix: "${GITHUB_REPOSITORY}/" }
    logs:  { type: s3, bucket: gha-logs,  prefix: "${GITHUB_REPOSITORY}/" }
    state: { type: postgres, url_source: state_db_url }

  kubernetes:
    detect:
      env_var: KUBERNETES_SERVICE_HOST
      present: true
    cache: { type: controller }
    logs:  { type: controller }
`)
	f, err := backends.Load(filepath.Join(dir, "backends.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Defaults.Cache.Path != "/var/cache/sparkwing" {
		t.Errorf("defaults.cache.path = %q", f.Defaults.Cache.Path)
	}
	if f.Defaults.Cache.Binaries == nil || f.Defaults.Cache.Binaries.Bucket != "sparkwing-binaries" {
		t.Errorf("binaries override not preserved: %+v", f.Defaults.Cache.Binaries)
	}
	if f.Defaults.State.Path != "/var/lib/sparkwing/state.db" {
		t.Errorf("defaults.state.path = %q", f.Defaults.State.Path)
	}
	gha, ok := f.Environments["gha"]
	if !ok || gha.Detect.Equals != "true" {
		t.Errorf("gha not parsed: %+v", gha)
	}
	if gha.Name != "gha" {
		t.Errorf("gha.Name = %q, want gha", gha.Name)
	}
	if gha.State.URLSource != "state_db_url" {
		t.Errorf("gha.state.url_source = %q", gha.State.URLSource)
	}
	k8s := f.Environments["kubernetes"]
	if !k8s.Detect.Present {
		t.Errorf("kubernetes.detect.present not set")
	}
	order := f.EnvironmentOrder()
	if !reflect.DeepEqual(order, []string{"gha", "kubernetes"}) {
		t.Errorf("environment order = %v, want [gha kubernetes]", order)
	}
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	f, err := backends.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if f.Defaults.Cache != nil || len(f.Environments) != 0 {
		t.Errorf("expected empty File, got %+v", f)
	}
}

func TestValidate_SurfaceAllowList(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "sqlite on logs",
			body: `
defaults:
  logs: { type: sqlite, path: /tmp/x }
`,
			wantErr: "type \"sqlite\" not allowed on logs",
		},
		{
			name: "filesystem on state",
			body: `
defaults:
  state: { type: filesystem, path: /tmp/x }
`,
			wantErr: "type \"filesystem\" not allowed on state",
		},
		{
			name: "stdout on cache",
			body: `
defaults:
  cache: { type: stdout }
`,
			wantErr: "type \"stdout\" not allowed on cache",
		},
		{
			name: "s3 missing bucket",
			body: `
defaults:
  cache: { type: s3 }
`,
			wantErr: "type=s3 requires bucket",
		},
		{
			name: "filesystem missing path",
			body: `
defaults:
  cache: { type: filesystem }
`,
			wantErr: "type=filesystem requires path",
		},
		{
			name: "postgres without url or url_source",
			body: `
defaults:
  state: { type: postgres }
`,
			wantErr: "type=postgres requires exactly one of url or url_source",
		},
		{
			name: "postgres with both url and url_source",
			body: `
defaults:
  state: { type: postgres, url: postgres://x, url_source: secret_x }
`,
			wantErr: "type=postgres requires exactly one of url or url_source",
		},
		{
			name: "binaries override on logs",
			body: `
defaults:
  logs: { type: filesystem, path: /tmp/x, binaries: { type: s3, bucket: b } }
`,
			wantErr: "binaries override is only valid on cache",
		},
		{
			name: "nested binaries",
			body: `
defaults:
  cache:
    type: filesystem
    path: /tmp/x
    binaries:
      type: s3
      bucket: b
      binaries: { type: s3, bucket: c }
`,
			wantErr: "nested binaries override is not allowed",
		},
		{
			name: "missing detect.env_var",
			body: `
environments:
  x:
    detect: { equals: "y" }
    cache: { type: filesystem, path: /tmp/x }
`,
			wantErr: "detect.env_var is required",
		},
		{
			name: "detect with no equals and no present",
			body: `
environments:
  x:
    detect: { env_var: FOO }
    cache: { type: filesystem, path: /tmp/x }
`,
			wantErr: "detect requires either equals or present",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "backends.yaml", tc.body)
			_, err := backends.Load(filepath.Join(dir, "backends.yaml"))
			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolve_RepoWinsPerField(t *testing.T) {
	dir := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(xdg, "sparkwing"), "backends.yaml", `
defaults:
  cache: { type: filesystem, path: /user/cache }
  logs:  { type: filesystem, path: /user/logs }
environments:
  shared:
    detect: { env_var: USER_ENV, equals: "y" }
    cache: { type: s3, bucket: user-bucket, prefix: user/ }
  user-only:
    detect: { env_var: U, equals: "1" }
    cache: { type: filesystem, path: /user/only }
`)
	writeFile(t, dir, "backends.yaml", `
defaults:
  cache: { type: filesystem, path: /repo/cache }
environments:
  shared:
    detect: { env_var: REPO_ENV, equals: "y" }
    cache: { type: s3, bucket: repo-bucket }
`)
	f, err := backends.Resolve(dir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if f.Defaults.Cache.Path != "/repo/cache" {
		t.Errorf("defaults.cache.path = %q, want /repo/cache (repo wins)", f.Defaults.Cache.Path)
	}
	if f.Defaults.Logs == nil || f.Defaults.Logs.Path != "/user/logs" {
		t.Errorf("defaults.logs = %+v, want /user/logs (filled from user)", f.Defaults.Logs)
	}
	shared := f.Environments["shared"]
	if shared.Detect.EnvVar != "REPO_ENV" {
		t.Errorf("shared.detect.env_var = %q, want REPO_ENV", shared.Detect.EnvVar)
	}
	if shared.Cache.Bucket != "repo-bucket" {
		t.Errorf("shared.cache.bucket = %q, want repo-bucket", shared.Cache.Bucket)
	}
	if shared.Cache.Prefix != "user/" {
		t.Errorf("shared.cache.prefix = %q, want user/ (filled from user)", shared.Cache.Prefix)
	}
	if _, ok := f.Environments["user-only"]; !ok {
		t.Errorf("user-only environment missing")
	}
}

func TestDetectEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "backends.yaml", `
environments:
  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    cache: { type: filesystem, path: /tmp/gha }
  k8s:
    detect: { env_var: KUBERNETES_SERVICE_HOST, present: true }
    cache: { type: filesystem, path: /tmp/k8s }
  none:
    detect: { env_var: MISSING, present: true }
    cache: { type: filesystem, path: /tmp/none }
`)
	f, err := backends.Load(filepath.Join(dir, "backends.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	t.Run("equals match", func(t *testing.T) {
		t.Setenv("GITHUB_ACTIONS", "true")
		os.Unsetenv("KUBERNETES_SERVICE_HOST")
		os.Unsetenv("MISSING")
		name, _, ok := backends.DetectEnvironment(f)
		if !ok || name != "gha" {
			t.Errorf("DetectEnvironment = (%q, %v), want (gha, true)", name, ok)
		}
	})
	t.Run("present match", func(t *testing.T) {
		t.Setenv("GITHUB_ACTIONS", "false")
		t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
		os.Unsetenv("MISSING")
		name, _, ok := backends.DetectEnvironment(f)
		if !ok || name != "k8s" {
			t.Errorf("DetectEnvironment = (%q, %v), want (k8s, true)", name, ok)
		}
	})
	t.Run("no match", func(t *testing.T) {
		os.Unsetenv("GITHUB_ACTIONS")
		os.Unsetenv("KUBERNETES_SERVICE_HOST")
		os.Unsetenv("MISSING")
		_, _, ok := backends.DetectEnvironment(f)
		if ok {
			t.Errorf("expected no match")
		}
	})
}

func TestEffective_Precedence(t *testing.T) {
	f := backends.File{
		Defaults: backends.Surfaces{
			Cache: &backends.Spec{Type: backends.TypeFilesystem, Path: "/d/cache"},
			Logs:  &backends.Spec{Type: backends.TypeFilesystem, Path: "/d/logs"},
			State: &backends.Spec{Type: backends.TypeSQLite, Path: "/d/state.db"},
		},
		Environments: map[string]backends.Environment{
			"gha": {
				Name: "gha",
				Surfaces: backends.Surfaces{
					Cache: &backends.Spec{Type: backends.TypeS3, Bucket: "env-cache"},
				},
			},
		},
	}
	target := backends.Surfaces{
		Logs: &backends.Spec{Type: backends.TypeS3, Bucket: "target-logs"},
	}
	eff := backends.Effective(f, "gha", target)
	if eff.Cache.Type != backends.TypeS3 || eff.Cache.Bucket != "env-cache" {
		t.Errorf("cache = %+v, want s3/env-cache", eff.Cache)
	}
	if eff.Logs.Type != backends.TypeS3 || eff.Logs.Bucket != "target-logs" {
		t.Errorf("logs = %+v, want s3/target-logs", eff.Logs)
	}
	if eff.State.Type != backends.TypeSQLite || eff.State.Path != "/d/state.db" {
		t.Errorf("state = %+v, want sqlite default", eff.State)
	}
}

func TestEffective_TargetPerFieldMerge(t *testing.T) {
	f := backends.File{
		Defaults: backends.Surfaces{
			Logs: &backends.Spec{Type: backends.TypeS3, Bucket: "base", Prefix: "base/"},
		},
	}
	target := backends.Surfaces{
		Logs: &backends.Spec{Type: backends.TypeS3, Prefix: "target/"},
	}
	eff := backends.Effective(f, "", target)
	if eff.Logs.Bucket != "base" {
		t.Errorf("bucket = %q, want base (filled from base)", eff.Logs.Bucket)
	}
	if eff.Logs.Prefix != "target/" {
		t.Errorf("prefix = %q, want target/ (target wins)", eff.Logs.Prefix)
	}
}

func TestEffective_TypeChangeResetsSpec(t *testing.T) {
	f := backends.File{
		Defaults: backends.Surfaces{
			Cache: &backends.Spec{Type: backends.TypeFilesystem, Path: "/d/cache"},
		},
	}
	target := backends.Surfaces{
		Cache: &backends.Spec{Type: backends.TypeS3, Bucket: "b"},
	}
	eff := backends.Effective(f, "", target)
	if eff.Cache.Type != backends.TypeS3 {
		t.Errorf("type = %q, want s3", eff.Cache.Type)
	}
	if eff.Cache.Path != "" {
		t.Errorf("path = %q, want empty (type change resets)", eff.Cache.Path)
	}
}
