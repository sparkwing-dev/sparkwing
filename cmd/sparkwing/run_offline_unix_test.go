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
		"GOPROXY=file://"+filepath.ToSlash(offlineHostModuleProxy(t)),
		"GOSUMDB=off",
	)
}

// offlineHostModuleProxy returns the host module cache's download tree, which is
// laid out as a module proxy. Seeding the first run from it keeps every module
// this fixture needs on local disk, so the run that populates the fixture cache
// reaches no network and the guarantee under test can be checked on a host that
// has none.
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

func offlineRunGo(goBin, dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(goBin, args...)
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
