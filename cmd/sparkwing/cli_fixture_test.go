package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

var (
	submitCLIOnce sync.Once
	submitCLIDir  string
	submitCLIBin  string
	submitCLIErr  error
)

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

func TestMain(m *testing.M) {
	code := m.Run()
	if err := os.RemoveAll(submitCLIDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if code == 0 {
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}
