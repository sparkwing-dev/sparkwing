//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

const offlineFixtureModule = "offlinefixture"

const offlineSparkModule = "example.test/offlinespark"

const offlineSparkGreeting = "offline spark ok"

const offlineMarkerEnv = "OFFLINE_FIXTURE_MARKER"

const offlineStopTimeout = 30 * time.Second

// TestRun_PinnedPipelineRunsWithTheNetworkDenied runs a pipeline whose spark is
// pinned to an exact tag twice: once where modules may be downloaded, then again
// where the only reachable module proxy and the only reachable HTTP proxy are a
// recorder that answers 502 and logs what asked. The second run has to be green.
//
// Denial and observation are one mechanism. GOPROXY is the recorder, so a module
// fetch is refused and logged by the same request; HTTP_PROXY and HTTPS_PROXY
// are the recorder, so git over https is too; GIT_CONFIG_GLOBAL and
// GIT_CONFIG_NOSYSTEM empty the git configuration that could rewrite an https
// remote into ssh. HOME and GOMODCACHE are the fixture's own, so every module
// byte the denied runs read was put there by the first run, and the first run
// takes those bytes from the host module cache rather than the network.
func TestRun_PinnedPipelineRunsWithTheNetworkDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("the offline guarantee downloads modules and compiles a pipeline binary; run without -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	cli := buildSubmitCLI(t)

	fixtureHome := t.TempDir()
	offlineUnlockModuleCache(t, filepath.Join(fixtureHome, "go", "pkg", "mod"))
	sparkwingHome := t.TempDir()
	t.Setenv("SPARKWING_HOME", sparkwingHome)
	offlineStopDaemon(t, sparkwingHome)

	// safety: a sparkwing on the developer's PATH would host the admission
	// daemon for this home, and the operator's binary is not this test's to
	// start; the fixture PATH carries the two tools a run actually needs.
	toolPath := offlineToolPath(t, goBin, gitBin)
	repoDir, sparkwingDir := offlineWriteFixture(t, gitBin, toolPath, fixtureHome)
	marker := filepath.Join(t.TempDir(), "ran.txt")

	connected := offlineConnectedEnv(t, fixtureHome, toolPath, sparkwingHome, marker)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, connected, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}

	firstOut, err := offlineRunCLI(cli, repoDir, connected, "run", "offline")
	if err != nil {
		t.Fatalf("first run failed after its modules downloaded: %v\n%s", err, firstOut)
	}
	if !strings.Contains(firstOut, "compiling .sparkwing/") {
		t.Fatalf("the first run did not compile the pipeline binary, so the second proves nothing:\n%s", firstOut)
	}

	denier := offlineNewDenier(t)
	denied := offlineDeniedEnv(t, fixtureHome, toolPath, sparkwingHome, marker, denier.url)

	cached := denier.mark()
	secondOut, err := offlineRunCLI(cli, repoDir, denied, "run", "offline")
	if err != nil {
		t.Fatalf("the second run failed with no network reachable: %v\n%s", err, secondOut)
	}
	if got := offlineMarkerLines(t, marker); len(got) != 2 || got[0] != offlineSparkGreeting || got[1] != offlineSparkGreeting {
		t.Fatalf("pipeline body wrote %q, want the spark greeting once per run", got)
	}
	if strings.Contains(secondOut, "compiling .sparkwing/") {
		t.Fatalf("the second run rebuilt the pipeline binary instead of reusing the cached one:\n%s", secondOut)
	}
	if reached := denier.since(cached); len(reached) > 0 {
		t.Fatalf("the second run tried to reach the network: %v\n%s", reached, secondOut)
	}

	read := denier.mark()
	listing, err := offlineRunCLI(cli, repoDir, denied, "runs", "list", "-o", "json")
	if err != nil {
		t.Fatalf("reading the run history with no network reachable: %v\n%s", err, listing)
	}
	sources := offlineBinarySources(t, listing)
	if len(sources) != 2 {
		t.Fatalf("run history holds %d runs, want the compiled one and the cached one: %v", len(sources), sources)
	}
	if sources[0] != "cached" || sources[1] != "compiled" {
		t.Fatalf("binary sources newest first = %v, want [cached compiled]", sources)
	}
	if reached := denier.since(read); len(reached) > 0 {
		t.Fatalf("reading the run history tried to reach the network: %v", reached)
	}

	// safety: a latest pin asks the module proxy, which is the recorder, so
	// this phase is also the proof that the denial is armed.
	offlineTrackLatest(t, sparkwingDir)
	tracking := denier.mark()
	trackingOut, err := offlineRunCLI(cli, repoDir, denied, "run", "offline")
	if err == nil {
		t.Fatalf("a latest pin resolved with no network reachable:\n%s", trackingOut)
	}
	if !strings.Contains(trackingOut, "--sw-no-update") {
		t.Fatalf("the offline failure does not name the flag that skips resolution:\n%s", trackingOut)
	}
	if len(denier.since(tracking)) == 0 {
		t.Fatalf("a latest pin reached no proxy, so the denied environment is not in the request path:\n%s", trackingOut)
	}

	// safety: the manifest changed, so this run compiles instead of reusing the
	// cached binary, and it must do so from the cache the first run filled.
	skipped := denier.mark()
	skippedOut, err := offlineRunCLI(cli, repoDir, denied, "run", "offline", "--sw-no-update")
	if err != nil {
		t.Fatalf("--sw-no-update did not carry a latest pin through an offline run: %v\n%s", err, skippedOut)
	}
	if !strings.Contains(skippedOut, "compiling .sparkwing/") {
		t.Fatalf("the offline compile did not happen, so it proves nothing about building without a proxy:\n%s", skippedOut)
	}
	if got := offlineMarkerLines(t, marker); len(got) != 3 {
		t.Fatalf("pipeline body ran %d times, want one per run", len(got))
	}
	if reached := denier.since(skipped); len(reached) > 0 {
		t.Fatalf("the offline compile tried to reach the network: %v\n%s", reached, skippedOut)
	}
}

type offlineDenier struct {
	url  string
	mu   sync.Mutex
	seen []string
}

func offlineNewDenier(t *testing.T) *offlineDenier {
	t.Helper()
	d := &offlineDenier{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.seen = append(d.seen, r.Method+" "+r.Host+r.URL.Path)
		d.mu.Unlock()
		http.Error(w, "this run has no network", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	d.url = server.URL
	return d
}

func (d *offlineDenier) mark() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func (d *offlineDenier) since(mark int) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen[mark:]...)
}

func offlineStopDaemon(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), offlineStopTimeout)
		defer cancel()
		err := wingdclient.Stop(ctx, wingdclient.Options{Home: home})
		if err == nil || errors.Is(err, wingdclient.ErrNoDaemon) {
			return
		}
		t.Errorf("admission daemon for %s outlived the test: %v", home, err)
	})
}

// safety: the module cache is written read-only, so the temporary directory
// holding it cannot be removed until every entry is writable again. This
// cleanup is registered after the directory's own, so it runs first.
func offlineUnlockModuleCache(t *testing.T, modCache string) {
	t.Helper()
	t.Cleanup(func() {
		err := filepath.WalkDir(modCache, func(path string, _ os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			return os.Chmod(path, 0o700)
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("make the fixture module cache removable: %v", err)
		}
	})
}

func offlineToolPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		if err := os.Symlink(tool, filepath.Join(dir, filepath.Base(tool))); err != nil {
			t.Fatalf("place %s on the fixture PATH: %v", tool, err)
		}
	}
	return dir
}

func offlineGoEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	if err != nil {
		t.Fatalf("resolve the host %s: %v", name, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		t.Fatalf("the host reports an empty %s", name)
	}
	return value
}

func offlineBaseEnv(t *testing.T, fixtureHome, toolPath, sparkwingHome, marker string) []string {
	t.Helper()
	return []string{
		"HOME=" + fixtureHome,
		"PATH=" + toolPath,
		"GOMODCACHE=" + filepath.Join(fixtureHome, "go", "pkg", "mod"),
		"GOCACHE=" + offlineGoEnv(t, "GOCACHE"),
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"SPARKWING_HOME=" + sparkwingHome,
		"SPARKWING_LOG_FORMAT=quiet",
		offlineMarkerEnv + "=" + marker,
	}
}

func offlineConnectedEnv(t *testing.T, fixtureHome, toolPath, sparkwingHome, marker string) []string {
	t.Helper()
	return append(offlineBaseEnv(t, fixtureHome, toolPath, sparkwingHome, marker),
		"GOFLAGS=-mod=mod",
		"GOPROXY="+offlineConnectedProxy(t),
		"GOSUMDB=off",
	)
}

// safety: the host module cache's download tree is laid out as a module proxy,
// so seeding the fixture from it leaves this suite runnable on a host with no
// network.
func offlineConnectedProxy(t *testing.T) string {
	t.Helper()
	return "file://" + filepath.ToSlash(offlineHostModuleProxy(t)) + ",https://proxy.golang.org,direct"
}

func offlineHostModuleProxy(t *testing.T) string {
	t.Helper()
	proxy := filepath.Join(offlineGoEnv(t, "GOMODCACHE"), "cache", "download")
	if _, statErr := os.Stat(proxy); statErr != nil {
		t.Fatalf("host module cache holds no download tree at %s: %v", proxy, statErr)
	}
	return proxy
}

func offlineDeniedEnv(t *testing.T, fixtureHome, toolPath, sparkwingHome, marker, recorder string) []string {
	t.Helper()
	return append(offlineBaseEnv(t, fixtureHome, toolPath, sparkwingHome, marker),
		"GOPROXY="+recorder,
		"GOFLAGS=-mod=readonly",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"HTTP_PROXY="+recorder,
		"HTTPS_PROXY="+recorder,
		"http_proxy="+recorder,
		"https_proxy="+recorder,
		"NO_PROXY=",
		"no_proxy=",
	)
}

func offlineMarkerLines(t *testing.T, marker string) []string {
	t.Helper()
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read the pipeline marker %s: %v", marker, err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func offlineRunCLI(cli, dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(cli, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func offlineRunTool(tool, dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func offlineBinarySources(t *testing.T, listing string) []string {
	t.Helper()
	var sources []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record struct {
			Invocation struct {
				BinarySource string `json:"binary_source"`
			} `json:"invocation"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode run record %q: %v", line, err)
		}
		sources = append(sources, record.Invocation.BinarySource)
	}
	return sources
}

func offlineWriteFixture(t *testing.T, gitBin, toolPath, fixtureHome string) (repoDir, sparkwingDir string) {
	t.Helper()
	root := t.TempDir()
	sparkDir := filepath.Join(root, "spark")
	repoDir = filepath.Join(root, "repo")
	sparkwingDir = filepath.Join(repoDir, ".sparkwing")
	for _, dir := range []string{sparkDir, filepath.Join(sparkwingDir, "jobs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	writeFile(t, filepath.Join(sparkDir, "go.mod"), "module "+offlineSparkModule+"\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(sparkDir, "spark.go"),
		"package offlinespark\n\nconst Greeting = \""+offlineSparkGreeting+"\"\n")

	writeFile(t, filepath.Join(sparkwingDir, "go.mod"), fmt.Sprintf(
		"module %s\n\ngo 1.26.0\n\nrequire (\n\t%s v0.1.0\n\tgithub.com/sparkwing-dev/sparkwing v0.0.0\n)\n\n"+
			"replace %s => %s\n\nreplace github.com/sparkwing-dev/sparkwing => %s\n",
		offlineFixtureModule, offlineSparkModule, offlineSparkModule, sparkDir, offlineRepoRoot(t)))
	writeFile(t, filepath.Join(sparkwingDir, "sparkwing.yaml"), fmt.Sprintf(
		"pipelines:\n  - name: offline\n    entrypoint: Offline\n    description: Runs with no network once its modules are on disk\n\n"+
			"sparks:\n  - name: offline-spark\n    source: %s\n    version: v0.1.0\n", offlineSparkModule))
	writeFile(t, filepath.Join(sparkwingDir, "jobs", "jobs.go"), offlineFixtureJobs)
	writeFile(t, filepath.Join(sparkwingDir, "main.go"), offlineFixtureMain)

	cmd := exec.Command(gitBin, "init", "-q")
	cmd.Dir = repoDir
	cmd.Env = []string{"HOME=" + fixtureHome, "PATH=" + toolPath, "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", repoDir, err, out)
	}
	return repoDir, sparkwingDir
}

func offlineTrackLatest(t *testing.T, sparkwingDir string) {
	t.Helper()
	path := filepath.Join(sparkwingDir, "sparkwing.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	tracking := strings.Replace(string(body), "version: v0.1.0", "version: latest", 1)
	if tracking == string(body) {
		t.Fatalf("%s carries no exact pin to loosen", path)
	}
	writeFile(t, path, tracking)
}

func offlineRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test's source path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

const offlineFixtureJobs = `package jobs

import (
	"context"
	"errors"
	"os"

	spark "example.test/offlinespark"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Offline struct{ sparkwing.Base }

func (p *Offline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "offline", func(ctx context.Context) error {
		sparkwing.Info(ctx, spark.Greeting)
		f, err := os.OpenFile(os.Getenv("OFFLINE_FIXTURE_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(spark.Greeting + "\n"); err != nil {
			return errors.Join(err, f.Close())
		}
		return f.Close()
	})
	return nil
}

func init() {
	sparkwing.Register("offline", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Offline{} })
}
`

const offlineFixtureMain = `package main

import (
	_ "offlinefixture/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() { runner.Main() }
`
