package runners_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/runners"
)

// withXDG points XDG_CONFIG_HOME at a temp directory and creates the
// sparkwing/ subdirectory so writes against UserConfigPath land there
// without surprising the developer's real home.
func withXDG(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "sparkwing"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return tmp
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoad_RoundTripsAllRunnerTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  local:
    type: local
    labels: [local, "os=darwin"]
  cloud-linux:
    type: kubernetes
    controller: shared
    labels: [cloud-linux, "os=linux"]
    spec:
      nodeSelector:
        karpenter.sh/nodepool: general
      tolerations:
        - key: nvidia.com/gpu
          operator: Exists
      resources:
        requests:
          cpu: "2"
          memory: 4Gi
        limits:
          memory: 8Gi
  mac-mini:
    type: static
    labels: [mac-mini, "os=macos", trusted]
`)

	f, err := runners.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(f.Runners); got != 3 {
		t.Fatalf("loaded %d runners, want 3", got)
	}

	local := f.Runners["local"]
	if local.Type != "local" {
		t.Errorf("local.Type = %q", local.Type)
	}
	if !reflect.DeepEqual(local.Labels, []string{"local", "os=darwin"}) {
		t.Errorf("local.Labels = %v", local.Labels)
	}

	k := f.Runners["cloud-linux"]
	if k.Type != "kubernetes" || k.Controller != "shared" {
		t.Errorf("cloud-linux Type/Controller = %q/%q", k.Type, k.Controller)
	}
	if k.Spec.NodeSelector["karpenter.sh/nodepool"] != "general" {
		t.Errorf("nodeSelector lost: %v", k.Spec.NodeSelector)
	}
	if len(k.Spec.Tolerations) != 1 || k.Spec.Tolerations[0].Key != "nvidia.com/gpu" {
		t.Errorf("tolerations lost: %+v", k.Spec.Tolerations)
	}
	if k.Spec.Resources.Requests["cpu"] != "2" || k.Spec.Resources.Requests["memory"] != "4Gi" {
		t.Errorf("requests lost: %v", k.Spec.Resources.Requests)
	}
	if k.Spec.Resources.Limits["memory"] != "8Gi" {
		t.Errorf("limits lost: %v", k.Spec.Resources.Limits)
	}

	s := f.Runners["mac-mini"]
	if s.Type != "static" {
		t.Errorf("mac-mini.Type = %q", s.Type)
	}
	if !reflect.DeepEqual(s.Labels, []string{"mac-mini", "os=macos", "trusted"}) {
		t.Errorf("mac-mini.Labels = %v", s.Labels)
	}
}

func TestLoad_FiltersEmptyLabels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  example:
    type: local
    labels: ["", "a", "", "b", ""]
`)
	f, err := runners.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := f.Runners["example"].Labels; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Labels = %v, want [a b]", got)
	}
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	f, err := runners.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(f.Runners) != 0 {
		t.Errorf("expected empty File, got %v", f.Runners)
	}
}

func TestLoad_UnknownFieldFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  x:
    type: local
    surprise: 1
`)
	_, err := runners.Load(path)
	if err == nil {
		t.Fatal("expected unknown-field parse error")
	}
}

func TestValidate_UnknownTypeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  weird:
    type: nomad
`)
	_, err := runners.Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("expected unknown-type error, got %v", err)
	}
}

func TestValidate_MissingType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  notyped:
    labels: [a]
`)
	_, err := runners.Load(path)
	if err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("expected type-required error, got %v", err)
	}
}

func TestValidate_KubernetesNeedsController(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  cloud:
    type: kubernetes
    labels: [cloud]
`)
	_, err := runners.Load(path)
	if err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("expected controller-required error, got %v", err)
	}
}

func TestValidate_SpecOnLocalRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  oops:
    type: local
    spec:
      nodeSelector: {os: linux}
`)
	_, err := runners.Load(path)
	if err == nil || !strings.Contains(err.Error(), "spec block") {
		t.Fatalf("expected spec-only-kubernetes error, got %v", err)
	}
}

func TestValidate_SpecOnStaticRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.yaml")
	writeFile(t, path, `
runners:
  oops:
    type: static
    spec:
      resources:
        requests: {cpu: "1"}
`)
	_, err := runners.Load(path)
	if err == nil || !strings.Contains(err.Error(), "spec block") {
		t.Fatalf("expected spec-only-kubernetes error, got %v", err)
	}
}

func TestResolve_RepoOnly(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, runners.RepoConfigPath(repoDir), `
runners:
  cloud:
    type: kubernetes
    controller: shared
    labels: [cloud]
`)
	r, ok, err := runners.Resolve(repoDir, "cloud")
	if err != nil || !ok {
		t.Fatalf("Resolve(cloud): %v ok=%v", err, ok)
	}
	if r.Type != "kubernetes" || r.Controller != "shared" {
		t.Errorf("got %+v", r)
	}
}

func TestResolve_UserOnly(t *testing.T) {
	xdg := withXDG(t)
	writeFile(t, filepath.Join(xdg, "sparkwing", "runners.yaml"), `
runners:
  personal:
    type: static
    labels: [personal, "host=korey-laptop"]
`)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	r, ok, err := runners.Resolve(repoDir, "personal")
	if err != nil || !ok {
		t.Fatalf("Resolve(personal): %v ok=%v", err, ok)
	}
	if r.Type != "static" {
		t.Errorf("got %+v", r)
	}
}

func TestResolve_RepoWinsPerField(t *testing.T) {
	xdg := withXDG(t)
	writeFile(t, filepath.Join(xdg, "sparkwing", "runners.yaml"), `
runners:
  cloud-linux:
    type: kubernetes
    controller: user-cluster
    labels: [user-default]
    spec:
      resources:
        requests: {cpu: "8"}
`)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, runners.RepoConfigPath(repoDir), `
runners:
  cloud-linux:
    type: kubernetes
    controller: shared
    labels: [cloud-linux, "os=linux"]
`)
	r, ok, err := runners.Resolve(repoDir, "cloud-linux")
	if err != nil || !ok {
		t.Fatalf("Resolve: %v ok=%v", err, ok)
	}
	// repo's controller wins
	if r.Controller != "shared" {
		t.Errorf("Controller = %q, want shared (repo)", r.Controller)
	}
	// repo's labels win (non-empty in repo)
	if !reflect.DeepEqual(r.Labels, []string{"cloud-linux", "os=linux"}) {
		t.Errorf("Labels = %v, want repo's value", r.Labels)
	}
	// repo left Spec blank; user value fills the blank
	if r.Spec.Resources.Requests["cpu"] != "8" {
		t.Errorf("Spec.Resources.Requests = %v, want user fill-in", r.Spec.Resources.Requests)
	}
}

func TestResolve_UnknownNameReturnsNotFound(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	r, ok, err := runners.Resolve(repoDir, "nope")
	if err != nil {
		t.Fatalf("Resolve(nope): %v", err)
	}
	if ok {
		t.Errorf("expected ok=false for unknown name, got %+v", r)
	}
}

func TestResolve_ImplicitLocalWhenAbsent(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	r, ok, err := runners.Resolve(repoDir, "local")
	if err != nil || !ok {
		t.Fatalf("Resolve(local) implicit: %v ok=%v", err, ok)
	}
	if r.Type != "local" || r.Name != "local" {
		t.Errorf("synthesized local malformed: %+v", r)
	}
	wantOS := "os=" + runtime.GOOS
	wantArch := "arch=" + runtime.GOARCH
	var sawOS, sawArch, sawLocal bool
	for _, l := range r.Labels {
		switch l {
		case wantOS:
			sawOS = true
		case wantArch:
			sawArch = true
		case "local":
			sawLocal = true
		}
	}
	if !sawOS || !sawArch || !sawLocal {
		t.Errorf("implicit local labels missing host info: %v", r.Labels)
	}
}

func TestResolve_UserLocalOverridesImplicit(t *testing.T) {
	xdg := withXDG(t)
	writeFile(t, filepath.Join(xdg, "sparkwing", "runners.yaml"), `
runners:
  local:
    type: local
    labels: [local, "host=mybox"]
`)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	r, ok, err := runners.Resolve(repoDir, "local")
	if err != nil || !ok {
		t.Fatalf("Resolve(local): %v ok=%v", err, ok)
	}
	if !reflect.DeepEqual(r.Labels, []string{"local", "host=mybox"}) {
		t.Errorf("user-declared local should win: %v", r.Labels)
	}
}

func TestNames_IncludesImplicitLocalWhenAbsent(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, runners.RepoConfigPath(repoDir), `
runners:
  cloud:
    type: kubernetes
    controller: shared
`)
	got, err := runners.Names(repoDir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	set := map[string]bool{}
	for _, n := range got {
		set[n] = true
	}
	if !set["local"] {
		t.Errorf("Names did not include implicit local: %v", got)
	}
	if !set["cloud"] {
		t.Errorf("Names did not include cloud: %v", got)
	}
	if got[0] != "local" {
		t.Errorf("local should appear first when synthesized, got %v", got)
	}
}

func TestNames_SkipsImplicitLocalWhenUserDeclaresOne(t *testing.T) {
	xdg := withXDG(t)
	writeFile(t, filepath.Join(xdg, "sparkwing", "runners.yaml"), `
runners:
  local:
    type: local
    labels: [local, custom]
`)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	got, err := runners.Names(repoDir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	count := 0
	for _, n := range got {
		if n == "local" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one 'local' entry, got %d in %v", count, got)
	}
}

func TestNames_BothFilesMissingReturnsLocalOnly(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	got, err := runners.Names(repoDir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"local"}) {
		t.Errorf("Names = %v, want [local]", got)
	}
}

func TestResolve_BothFilesMissingNameLookupNotLocal(t *testing.T) {
	_ = withXDG(t)
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	r, ok, err := runners.Resolve(repoDir, "anything")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Errorf("expected not-found, got %+v", r)
	}
}
