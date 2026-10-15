package main

import (
	"bytes"
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

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/testleak"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var (
	submitCLIOnce sync.Once
	submitCLIDir  string
	submitCLIBin  string
	submitCLIErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(submitCLIDir)
	if err := killFixtureSurvivors(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	} else if code == 0 {
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

const fixtureExitGrace = 10 * time.Second

// safety: a supervisor a test leaves behind is reparented to init and keeps
// respawning its worker under a home the suite has already deleted.
func killFixtureSurvivors() error {
	deadline := time.Now().Add(fixtureExitGrace)
	survivors := fixtureProcesses()
	for len(survivors) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		survivors = fixtureProcesses()
	}
	if len(survivors) == 0 {
		return nil
	}
	for _, pid := range survivors {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
	return fmt.Errorf("fixture CLI processes outlived the suite: %v", survivors)
}

func fixtureProcesses() []int {
	if submitCLIDir == "" {
		return nil
	}
	out, err := exec.Command("ps", "-eww", "-o", "pid=,args=").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture survivor check disarmed: ps: %v\n", err)
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, submitCLIDir) {
			continue
		}
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(line), &pid); err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

func buildSubmitCLI(t *testing.T) string {
	t.Helper()
	submitCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sparkwing-submit-cli")
		if err != nil {
			submitCLIErr = err
			return
		}
		submitCLIDir = dir
		bin := filepath.Join(dir, "sparkwing")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		overlay, err := seededBundleOverlay(dir)
		if err != nil {
			submitCLIErr = err
			return
		}
		cmd := exec.Command("go", "build", "-overlay", overlay, "-o", bin, "github.com/sparkwing-dev/sparkwing/cmd/sparkwing")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			submitCLIErr = err
			return
		}
		submitCLIBin = bin
	})
	if submitCLIErr != nil {
		t.Fatalf("build sparkwing CLI: %v", submitCLIErr)
	}
	return submitCLIBin
}

// safety: a reproducible build trims the compiled-in source path, so resolving the
// repository from runtime.Caller yields a module path the filesystem does not hold.
// This resolves at package initialization because tests in this package change the
// working directory, and a later walk would start from wherever one of them left it.
var moduleRootDir, moduleRootErr = func() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}()

func moduleRoot() (string, error) {
	return moduleRootDir, moduleRootErr
}

// safety: the dashboard bundle is a gitignored artifact of bin/build-web.sh, so
// without a stub this binary's dashboard depends on the developer's build state.
func seededBundleOverlay(dir string) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(root, "internal", "web", "next-out")); err != nil {
		return "", fmt.Errorf("locate the embedded dashboard directory: %w", err)
	}
	index := filepath.Join(dir, "index.html")
	if err = os.WriteFile(index, []byte("<!doctype html><title>sparkwing test bundle</title>\n"), 0o600); err != nil {
		return "", fmt.Errorf("write stub dashboard index: %w", err)
	}
	document, err := json.Marshal(map[string]map[string]string{
		"Replace": {filepath.Join(root, "internal", "web", "next-out", "index.html"): index},
	})
	if err != nil {
		return "", fmt.Errorf("encode build overlay: %w", err)
	}
	overlay := filepath.Join(dir, "bundle-overlay.json")
	if err = os.WriteFile(overlay, document, 0o600); err != nil {
		return "", fmt.Errorf("write build overlay: %w", err)
	}
	return overlay, nil
}

const submitFixtureSource = `package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--sw-node-protocol" {
		_, _ = bufio.NewReader(os.NewFile(3, "requests")).ReadString('\n')
		fmt.Fprintln(os.NewFile(4, "frames"), "{\"reply\":\"1\",\"ok\":true,\"result\":{\"protocol\":2,\"sdk\":{\"language\":\"go\",\"version\":\"fixture\"},\"features\":[]}}"); _, _ = bufio.NewReader(os.NewFile(3, "requests")).ReadString('\n'); fmt.Fprintln(os.NewFile(4, "frames"), "{\"reply\":\"2\",\"ok\":true,\"result\":{\"pipelines\":[{\"name\":\"fixture\",\"args\":[]}]}}")
		return
	}
	f, err := os.OpenFile(os.Getenv("SPARKWING_SUBMIT_TEST_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, os.Args[len(os.Args)-1])
	if envMarker := os.Getenv("SPARKWING_SUBMIT_TEST_ENV_MARKER"); envMarker != "" {
		ef, err := os.OpenFile(envMarker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			panic(err)
		}
		defer ef.Close()
		fmt.Fprintln(ef, os.Getenv("SPARKWING_SUBMIT_TEST_ENV"))
	}
}
`

type submitTestEnv struct {
	t         *testing.T
	bin       string
	home      string
	repoDir   string
	marker    string
	envMarker string
	extraEnv  []string
}

func newSubmitTestEnv(t *testing.T) *submitTestEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the detached-consumer contract is exercised on POSIX process semantics")
	}

	home, err := os.MkdirTemp("", "swh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	repoDir := t.TempDir()
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.mod"),
		[]byte("module submitfixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"),
		[]byte(submitFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &submitTestEnv{
		t:         t,
		bin:       buildSubmitCLI(t),
		home:      home,
		repoDir:   repoDir,
		marker:    filepath.Join(repoDir, "dispatched.txt"),
		envMarker: filepath.Join(repoDir, "environment.txt"),
	}
	t.Cleanup(env.stopDaemon)
	return env
}

func (e *submitTestEnv) env() []string {
	base := append(os.Environ(),
		"SPARKWING_HOME="+e.home,

		"SPARKWING_CONFIG="+filepath.Join(e.home, "config.yaml"),
		"SPARKWING_NO_UPDATE=1",
		"SPARKWING_SUBMIT_TEST_MARKER="+e.marker,
		"SUBMIT_TEST_MARKER="+e.marker,
		"SPARKWING_SUBMIT_TEST_ENV_MARKER="+e.envMarker,
	)
	return append(base, e.extraEnv...)
}

func (e *submitTestEnv) run(args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env()
	cmd.Dir = e.repoDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *submitTestEnv) runStdout(args ...string) (string, string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env()
	cmd.Dir = e.repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (e *submitTestEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("sparkwing %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// detachArgs spells one detached launch: every sparkwing flag follows the
// pipeline name, and SPARKWING_HOME in the test environment selects the home.
func (e *submitTestEnv) detachArgs(pipeline string, extra ...string) []string {
	args := []string{"-C", e.repoDir, "run", pipeline, "--sw-detached"}
	return append(args, extra...)
}

func (e *submitTestEnv) submit(extra ...string) submitResult {
	e.t.Helper()
	return e.submitWithArgs(extra, nil)
}

func (e *submitTestEnv) submitWithArgs(own, pipelineArgs []string) submitResult {
	e.t.Helper()
	args := append(e.detachArgs("fixture", "--sw-output", "json"), own...)
	args = append(args, pipelineArgs...)
	out, errOut, err := e.runStdout(args...)
	if err != nil {
		e.t.Fatalf("sparkwing %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, out, errOut)
	}
	var r submitResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		e.t.Fatalf("decode submit ack: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	return r
}

func (e *submitTestEnv) store() *store.Store {
	e.t.Helper()
	st, err := store.Open(orchestrator.PathsAt(e.home).StateDB())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = st.Close() })
	return st
}

func (e *submitTestEnv) markerLines() []string {
	return linesIn(e.marker)
}

func linesIn(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (e *submitTestEnv) stopDaemon() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := wingdclient.Stop(ctx, wingdclient.Options{Home: e.home}); err != nil &&
		!errors.Is(err, wingdclient.ErrNoDaemon) {
		e.t.Errorf("stop wingd for home %s: %v", e.home, err)
	}
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
	}
}

const blockingFixtureSource = `package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Fixture struct{ sparkwing.Base }

func (*Fixture) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(p, "work", hold)
	return nil
}

func hold(ctx context.Context) error {
	marker := os.Getenv("SPARKWING_SUBMIT_TEST_MARKER")
	id := os.Getenv("SPARKWING_RUN_ID")
	appendLine(marker, "START "+id+" pid="+strconv.Itoa(os.Getpid()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, os.Getenv("ADV_HOLD_URL"), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	appendLine(marker, "END "+id+" pid="+strconv.Itoa(os.Getpid()))
	return nil
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func main() {
	sparkwing.Register("fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Fixture{} })
	sparkwing.Main()
}
`

// safety: the engine host plans and runs the pipeline it is handed, so a test that waits on execution needs one
// built against this module's SDK; its one node records the run id and the submitted environment.
const hostedFixtureSource = `package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Fixture struct{ sparkwing.Base }

func (*Fixture) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(p, "work", func(context.Context) error {
		appendLine(os.Getenv("SPARKWING_SUBMIT_TEST_MARKER"), os.Getenv("SPARKWING_RUN_ID"))
		if marker := os.Getenv("SPARKWING_SUBMIT_TEST_ENV_MARKER"); marker != "" {
			appendLine(marker, os.Getenv("SPARKWING_SUBMIT_TEST_ENV"))
		}
		return nil
	})
	return nil
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func main() {
	sparkwing.Register("fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Fixture{} })
	sparkwing.Main()
}
`

func (e *submitTestEnv) useHostedFixture(t *testing.T, source string) {
	t.Helper()
	writeSDKFixture(t, filepath.Join(e.repoDir, ".sparkwing"), source)
}

func writeSDKFixture(t *testing.T, sparkwingDir, source string) {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			body = []byte(strings.Replace(string(body), "module github.com/sparkwing-dev/sparkwing", "module sdkfixture", 1) +
				fmt.Sprintf("\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\nreplace github.com/sparkwing-dev/sparkwing => %q\n", root))
		}
		if err := os.WriteFile(filepath.Join(sparkwingDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *submitTestEnv) useBlockingFixture(t *testing.T) <-chan struct{} {
	t.Helper()
	e.useHostedFixture(t, blockingFixtureSource)
	started := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	e.extraEnv = append(e.extraEnv, "ADV_HOLD_URL="+server.URL)
	if err := os.WriteFile(filepath.Join(e.home, "config.yaml"), []byte("run:\n  submit_env_allow: [ADV_HOLD_URL]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return started
}

func waitForFixtureHold(t *testing.T, started <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-started:
	case <-timer.C:
		t.Fatal("fixture did not enter its blocking request within 10s")
	}
}

func (e *submitTestEnv) startsInMarker() int {
	n := 0
	for _, l := range e.markerLines() {
		if strings.HasPrefix(l, "START ") {
			n++
		}
	}
	return n
}

func trigArgsHas(t *testing.T, e *submitTestEnv, runID, key string) (string, bool) {
	t.Helper()
	trig, err := e.store().GetTrigger(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := trig.Args[key]
	return v, ok
}
