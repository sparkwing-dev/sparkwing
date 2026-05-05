package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireDocker skips the test when `docker` is not on PATH.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not on PATH")
	}
}

// dockerDaemonUp performs a cheap `docker version` to verify the
// daemon is reachable. docker may be installed without a running
// daemon (CI sandbox case); those tests skip.
func dockerDaemonUp(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// withRepo creates a fresh git repo in a temp dir and chdirs into it
// for the duration of the test. Mirrors pkg/sparkwing/git's helper.
func withRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	runHost(t, "git", "init", "--initial-branch=main", ".")
	runHost(t, "git", "config", "user.email", "test@example.com")
	runHost(t, "git", "config", "user.name", "Test")
	runHost(t, "git", "config", "commit.gpgsign", "false")
	return dir
}

func runHost(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func commit(t *testing.T, msg string) {
	t.Helper()
	runHost(t, "git", "add", "-A")
	runHost(t, "git", "commit", "-m", msg)
}

func TestErrDockerUnavailable(t *testing.T) {
	// Point PATH at an empty temp dir so `docker` is not findable.
	t.Setenv("PATH", t.TempDir())

	ctx := context.Background()

	if _, err := Build(ctx, BuildConfig{Image: "x", Tags: []string{"t"}}); !errors.Is(err, ErrDockerUnavailable) {
		t.Errorf("Build: got %v, want ErrDockerUnavailable", err)
	}
	if _, err := BuildAndPush(ctx, BuildConfig{Image: "x", Tags: []string{"t"}}); !errors.Is(err, ErrDockerUnavailable) {
		t.Errorf("BuildAndPush: got %v, want ErrDockerUnavailable", err)
	}
	if err := Push(ctx, "x:t", []string{"t"}, []string{"r"}); !errors.Is(err, ErrDockerUnavailable) {
		t.Errorf("Push: got %v, want ErrDockerUnavailable", err)
	}
	if err := Login(ctx, "r", "u", "s"); !errors.Is(err, ErrDockerUnavailable) {
		t.Errorf("Login: got %v, want ErrDockerUnavailable", err)
	}
}

// fakeBuildxInspectDocker installs a `docker` shim on PATH that:
//   - exits 0 for `docker buildx version`
//   - prints the supplied buildx-inspect text on `docker buildx inspect`
//   - exits 0 for any other invocation (so a downstream `docker build`
//     in the same test sees a no-op success)
//
// Returns the bin dir for the caller to inspect call logs if needed.
func fakeBuildxInspectDocker(t *testing.T, inspectOutput string) string {
	t.Helper()
	dir := t.TempDir()
	// Marker file so the script can locate its inspect payload.
	payload := filepath.Join(dir, "inspect.txt")
	if err := os.WriteFile(payload, []byte(inspectOutput), 0o644); err != nil {
		t.Fatalf("write inspect payload: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "buildx" ] && [ "$2" = "version" ]; then
  exit 0
fi
if [ "$1" = "buildx" ] && [ "$2" = "inspect" ]; then
  /bin/cat %q
  exit 0
fi
exit 0
`, payload)
	fakeDocker := filepath.Join(dir, "docker")
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestParseBuildxPlatforms(t *testing.T) {
	// Realistic shape from `docker buildx inspect` on Docker Desktop:
	// native marker `*`, amd64 instruction-set variants, multiple
	// `Platforms:` lines (multi-node builders).
	raw := `Name:          default
Driver:        docker

Nodes:
Name:              default
Endpoint:          default
Status:            running
Buildkit version:  v0.13.1
Platforms:         linux/arm64*, linux/amd64, linux/amd64/v2, linux/amd64/v3, linux/386
Name:              extra
Endpoint:          tcp://10.0.0.1:2376
Status:            running
Platforms:         linux/arm/v7, linux/arm/v6
`
	got := parseBuildxPlatforms(raw)
	want := []string{
		"linux/arm64",
		"linux/amd64",
		"linux/amd64/v2",
		"linux/amd64/v3",
		"linux/386",
		"linux/arm/v7",
		"linux/arm/v6",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

func TestPlatformAdvertised(t *testing.T) {
	advertised := []string{"linux/arm64", "linux/amd64", "linux/amd64/v3"}

	cases := []struct {
		wish string
		want bool
	}{
		{"linux/arm64", true},
		{"linux/amd64", true},
		{"linux/amd64/v3", true},
		{"linux/386", false},
		{"linux/arm", false}, // bare "arm" must not be matched by "arm64"
	}
	for _, c := range cases {
		if got := platformAdvertised(c.wish, advertised); got != c.want {
			t.Errorf("platformAdvertised(%q, %v) = %v, want %v", c.wish, advertised, got, c.want)
		}
	}

	// `arm64` should NOT match `arm/v7`-style entries (no false-positive
	// from a substring or unintended prefix).
	if platformAdvertised("linux/arm", []string{"linux/arm64"}) {
		t.Errorf("linux/arm spuriously matched against [linux/arm64]")
	}
}

func TestBuildxPlatforms(t *testing.T) {
	fakeBuildxInspectDocker(t, `Nodes:
Platforms: linux/arm64*, linux/amd64
`)
	got, err := BuildxPlatforms(context.Background())
	if err != nil {
		t.Fatalf("BuildxPlatforms: %v", err)
	}
	want := []string{"linux/arm64", "linux/amd64"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilterBuildxPlatforms(t *testing.T) {
	// Builder advertises arm64 only (mimics arm64 cluster without QEMU).
	fakeBuildxInspectDocker(t, `Nodes:
Platforms: linux/arm64*
`)
	got, err := FilterBuildxPlatforms(context.Background(), []string{"linux/arm64", "linux/amd64"})
	if err != nil {
		t.Fatalf("FilterBuildxPlatforms: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"linux/arm64"}) {
		t.Errorf("got %v, want [linux/arm64]", got)
	}
}

func TestBuildPreFlightUnsupportedPlatform(t *testing.T) {
	// Builder advertises arm64 only; caller asks for amd64. Build must
	// reject before invoking buildx, citing ErrPlatformUnsupported.
	fakeBuildxInspectDocker(t, `Nodes:
Platforms: linux/arm64*
`)
	_, err := Build(context.Background(), BuildConfig{
		Image:     "x",
		Tags:      []string{"t"},
		Platforms: []string{"linux/amd64"},
	})
	if !errors.Is(err, ErrPlatformUnsupported) {
		t.Fatalf("got %v, want ErrPlatformUnsupported", err)
	}
	// Error message should name the missing platform and the advertised
	// list so a human can act on it.
	msg := err.Error()
	for _, want := range []string{"linux/amd64", "linux/arm64", "QEMU"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestErrBuildxRequired(t *testing.T) {
	// Put a fake `docker` on PATH that succeeds for every call except
	// `docker buildx version`, which exits non-zero. Build called with
	// non-empty Platforms must return ErrBuildxRequired.
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "buildx" ]; then
  exit 1
fi
exit 0
`
	fakeDocker := filepath.Join(dir, "docker")
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir)

	_, err := Build(context.Background(), BuildConfig{
		Image:     "x",
		Tags:      []string{"t"},
		Platforms: []string{"linux/amd64", "linux/arm64"},
	})
	if !errors.Is(err, ErrBuildxRequired) {
		t.Fatalf("got %v, want ErrBuildxRequired", err)
	}
}

func TestLoginSecretNotInArgv(t *testing.T) {
	// Fake `docker` that writes its argv and stdin to files in a spy
	// dir, then exits 0. Login(..., secret) must never appear in argv.
	spyDir := t.TempDir()
	argvFile := filepath.Join(spyDir, "argv")
	stdinFile := filepath.Join(spyDir, "stdin")

	binDir := t.TempDir()
	// Absolute paths to /bin/cat and /bin/echo so the script works
	// even when PATH is restricted (Login is invoked with the caller's
	// PATH, which isn't guaranteed to include /bin or /usr/bin in tests).
	script := fmt.Sprintf(`#!/bin/sh
/bin/cat > %q
for a in "$@"; do
  printf '%%s\n' "$a" >> %q
done
exit 0
`, stdinFile, argvFile)
	fakeDocker := filepath.Join(binDir, "docker")
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir)

	const secret = "s3cret-do-not-leak"
	if err := Login(context.Background(), "registry.example.com", "alice", secret); err != nil {
		t.Fatalf("Login: %v", err)
	}

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv spy: %v", err)
	}
	argvStr := string(argv)
	if strings.Contains(argvStr, secret) {
		t.Errorf("secret leaked into argv:\n%s", argvStr)
	}
	// Positive assertions on the shape we do want.
	for _, want := range []string{"login", "--username", "alice", "--password-stdin", "registry.example.com"} {
		if !strings.Contains(argvStr, want) {
			t.Errorf("argv missing %q:\n%s", want, argvStr)
		}
	}

	got, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin spy: %v", err)
	}
	if string(got) != secret {
		t.Errorf("stdin = %q, want %q", string(got), secret)
	}
}

func TestPushComputesCorrectArgs(t *testing.T) {
	// Fake docker that records every invocation to a log file. No real
	// daemon required.
	spyDir := t.TempDir()
	logFile := filepath.Join(spyDir, "calls")

	binDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
exit 0
`, logFile)
	fakeDocker := filepath.Join(binDir, "docker")
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir)

	err := Push(context.Background(), "myapp:latest", []string{"v1", "v2"}, []string{"reg1.example.com", "reg2.example.com"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	body, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(body)), "\n")
	// Expect 2 tag + 2 push per registry = 8 total calls.
	if len(calls) != 8 {
		t.Fatalf("expected 8 docker calls, got %d:\n%s", len(calls), strings.Join(calls, "\n"))
	}

	wantRefs := []string{
		"reg1.example.com/myapp:v1",
		"reg1.example.com/myapp:v2",
		"reg2.example.com/myapp:v1",
		"reg2.example.com/myapp:v2",
	}
	all := strings.Join(calls, "\n")
	for _, ref := range wantRefs {
		if !strings.Contains(all, "tag myapp:latest "+ref) {
			t.Errorf("missing tag call for %s", ref)
		}
		if !strings.Contains(all, "push "+ref) {
			t.Errorf("missing push call for %s", ref)
		}
	}
}

func TestComputeTags(t *testing.T) {
	withRepo(t)
	writeFile(t, "a.txt", "hello")
	commit(t, "init")

	ctx := context.Background()

	tag, err := ComputeTags(ctx)
	if err != nil {
		t.Fatalf("ComputeTags: %v", err)
	}

	if tag.Commit == "" {
		t.Errorf("Commit empty")
	}
	if tag.Content == "" {
		t.Errorf("Content empty")
	}
	if tag.Dirty {
		t.Errorf("clean tree reported dirty")
	}
	if tag.Branch != "main" {
		t.Errorf("Branch = %q, want main", tag.Branch)
	}

	want := "commit-" + tag.Commit + "-files-" + tag.Content
	if tag.DeployTag() != want {
		t.Errorf("DeployTag = %q, want %q", tag.DeployTag(), want)
	}
	if tag.ProdTag() != want+"-prod" {
		t.Errorf("ProdTag = %q, want %q", tag.ProdTag(), want+"-prod")
	}
	if all := tag.All(); len(all) != 2 || all[0] != tag.DeployTag() || all[1] != tag.ProdTag() {
		t.Errorf("All = %v, want [DeployTag, ProdTag]", all)
	}

	// Dirty the tree and recompute.
	writeFile(t, "a.txt", "modified")
	tag, err = ComputeTags(ctx)
	if err != nil {
		t.Fatalf("ComputeTags dirty: %v", err)
	}
	if !tag.Dirty {
		t.Errorf("expected dirty tree")
	}
	if !strings.HasSuffix(tag.DeployTag(), "-dirty") {
		t.Errorf("dirty DeployTag = %q, want -dirty suffix", tag.DeployTag())
	}
	if !strings.HasSuffix(tag.ProdTag(), "-dirty-prod") {
		t.Errorf("dirty ProdTag = %q, want -dirty-prod suffix", tag.ProdTag())
	}
}

func TestComputeTagsOnNonMainBranch(t *testing.T) {
	withRepo(t)
	writeFile(t, "a.txt", "hello")
	commit(t, "init")
	runHost(t, "git", "checkout", "-b", "feature/weird-name")

	ctx := context.Background()
	tag, err := ComputeTags(ctx)
	if err != nil {
		t.Fatalf("ComputeTags: %v", err)
	}
	if tag.Branch != "feature/weird-name" {
		t.Errorf("Branch = %q, want feature/weird-name", tag.Branch)
	}
}

func TestComputeTagsDetachedHead(t *testing.T) {
	withRepo(t)
	writeFile(t, "a.txt", "hello")
	commit(t, "init")

	// Detach HEAD at the current commit.
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(out))
	runHost(t, "git", "checkout", "--detach", sha)

	ctx := context.Background()
	tag, err := ComputeTags(ctx)
	if err != nil {
		t.Fatalf("ComputeTags detached: %v", err)
	}
	if tag.Branch != "" {
		t.Errorf("detached Branch = %q, want empty", tag.Branch)
	}
	if tag.Commit == "" {
		t.Errorf("detached Commit must still be set")
	}
}

func TestBuildRejectsMissingImage(t *testing.T) {
	requireDocker(t)
	_, err := Build(context.Background(), BuildConfig{Tags: []string{"t"}})
	if err == nil || !strings.Contains(err.Error(), "Image") {
		t.Fatalf("got %v, want missing-Image error", err)
	}
}

func TestBuildRejectsMissingTags(t *testing.T) {
	requireDocker(t)
	_, err := Build(context.Background(), BuildConfig{Image: "x"})
	if err == nil || !strings.Contains(err.Error(), "Tags") {
		t.Fatalf("got %v, want missing-Tags error", err)
	}
}

// TestBuildDefaults exercises the real docker daemon. Skips if docker
// is not available or not running.
func TestBuildDefaults(t *testing.T) {
	requireDocker(t)
	if !dockerDaemonUp(t) {
		t.Skip("docker daemon not reachable")
	}

	dir := t.TempDir()
	// Minimal Dockerfile using scratch to avoid network pulls when
	// possible. scratch exists without any registry round-trip.
	dockerfile := filepath.Join(dir, "Dockerfile")
	writeFile(t, dockerfile, "FROM scratch\nLABEL sparkwing-test=1\n")

	ctx := context.Background()
	image := fmt.Sprintf("sparkwing-test-%d", os.Getpid())
	tag := fmt.Sprintf("t%d", os.Getpid())

	res, err := Build(ctx, BuildConfig{
		Image:      image,
		Dockerfile: dockerfile,
		Context:    dir,
		Tags:       []string{tag},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Image == "" {
		t.Errorf("empty Image in result")
	}
	// Cleanup.
	_ = exec.Command("docker", "rmi", "-f", fmt.Sprintf("%s:%s", image, tag)).Run()
}

// TestBuildAndPushLocalRegistry exercises BuildAndPush against a local
// registry:2 container on a known port (default 5000). Skipped unless
// a registry is reachable at $SPARKWING_TEST_REGISTRY (e.g. "localhost:5000").
func TestBuildAndPushLocalRegistry(t *testing.T) {
	requireDocker(t)
	if !dockerDaemonUp(t) {
		t.Skip("docker daemon not reachable")
	}
	reg := os.Getenv("SPARKWING_TEST_REGISTRY")
	if reg == "" {
		t.Skip("SPARKWING_TEST_REGISTRY not set; skipping push end-to-end")
	}
	// Sanity-probe the registry's /v2/ endpoint.
	if !registryReachable(reg) {
		t.Skipf("registry %s not reachable", reg)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM scratch\nLABEL sparkwing-test=1\n")

	ctx := context.Background()
	image := fmt.Sprintf("sparkwing-push-%d-%s", os.Getpid(), runtime.GOARCH)
	tag := fmt.Sprintf("t%d", os.Getpid())

	res, err := BuildAndPush(ctx, BuildConfig{
		Image:      image,
		Context:    dir,
		Tags:       []string{tag},
		Registries: []string{reg},
	})
	if err != nil {
		t.Fatalf("BuildAndPush: %v", err)
	}
	if len(res.Registries) != 1 {
		t.Errorf("Registries = %v, want [%s]", res.Registries, reg)
	}
	if res.Image == "" || !strings.Contains(res.Image, reg) {
		t.Errorf("Image = %q, want to contain %q", res.Image, reg)
	}

	// Cleanup local refs.
	_ = exec.Command("docker", "rmi", "-f",
		fmt.Sprintf("%s:%s", image, tag),
		fmt.Sprintf("%s/%s:%s", reg, image, tag),
	).Run()
}

// registryReachable does a GET /v2/ against the registry.
func registryReachable(registry string) bool {
	u := &url.URL{Scheme: "http", Host: registry, Path: "/v2/"}
	req, _ := http.NewRequest("GET", u.String(), nil)
	resp, err := httpDefaultClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// 200 or 401 are both healthy signals for a registry:2.
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
}

// httpDefaultClient is split out so tests can swap it under
// httptest when needed.
func httpDefaultClient() *http.Client {
	return &http.Client{}
}

// Ensure httptest is referenced to satisfy the import on platforms
// where the end-to-end path is skipped; tests that reach the registry
// would otherwise leave httptest unused.
var _ = httptest.NewServer
