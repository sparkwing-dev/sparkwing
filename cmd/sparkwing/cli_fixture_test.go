package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testleak"
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

var (
	sdkFixtureDir                 string
	sdkFixtureEnvironment         = os.Environ()
	sdkFixtureGo, sdkFixtureGoErr = exec.LookPath("go")
)

// perf: hash only the SDK's build inputs, and let installer re-execs reuse its compiled fixtures.
var sdkFixtureModule = sync.OnceValues(func() (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	if sdkFixtureGoErr != nil {
		return "", sdkFixtureGoErr
	}
	sdkFixtureDir, err = os.MkdirTemp("", "sparkwing-sdk-fixtures-")
	if err != nil {
		return "", err
	}
	root = filepath.Join(root, "sparkwing")
	stage := filepath.Join(sdkFixtureDir, "sdk")
	list := exec.Command(sdkFixtureGo, "list", "-deps", "-json", "./...")
	list.Dir, list.Env = root, sdkFixtureEnvironment
	output, err := list.Output()
	if err != nil {
		return "", fmt.Errorf("list SDK fixture inputs: %w", err)
	}
	copyInput := func(input string) error {
		relative, err := filepath.Rel(root, input)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("SDK fixture input escaped module: %s", input)
		}
		body, err := os.ReadFile(input)
		if err != nil {
			return err
		}
		destination := filepath.Join(stage, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		return os.WriteFile(destination, body, 0o600)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for decoder.More() {
		var pkg struct {
			Dir                           string
			Module                        *struct{ Path string }
			GoFiles, CgoFiles, EmbedFiles []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			return "", err
		}
		if pkg.Module == nil || pkg.Module.Path != engine.GoModule {
			continue
		}
		for _, file := range append(append(pkg.GoFiles, pkg.CgoFiles...), pkg.EmbedFiles...) {
			if err := copyInput(filepath.Join(pkg.Dir, file)); err != nil {
				return "", err
			}
		}
	}
	for _, file := range []string{"go.mod", "go.sum"} {
		if err := copyInput(filepath.Join(root, file)); err != nil {
			return "", err
		}
	}
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	return stage, os.WriteFile(filepath.Join(sdkFixtureDir, "test-binary"), []byte(binary), 0o600)
})

func sdkFixtureModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := sdkFixtureModule()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func reuseSDKFixtureCache() error {
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	pin, err := engine.ReadPin(directory, engineid.GoSDK)
	if err != nil || !filepath.IsAbs(pin.Replacement) {
		return nil //nolint:nilerr // safety: the installer owns pin validation, including help without a module.
	}
	owner := filepath.Dir(pin.Replacement)
	marker, err := os.ReadFile(filepath.Join(owner, "test-binary"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if string(marker) != binary {
		return nil
	}
	// perf: testleak's marker is filtered from installer environments, so each re-exec has a fresh home.
	return os.Setenv("SPARKWING_HOME", filepath.Join(owner, "home"))
}

func writeSDKFixtureModule(t *testing.T, sparkwingDir string) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	if err := os.MkdirAll(sparkwingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := sdkFixtureModuleRoot(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			body = []byte(strings.Replace(string(body), "module github.com/sparkwing-dev/sparkwing/sparkwing", "module sdkfixture", 1) +
				fmt.Sprintf("\nrequire github.com/sparkwing-dev/sparkwing/sparkwing v0.0.0\nreplace github.com/sparkwing-dev/sparkwing/sparkwing => %q\n", root))
		}
		if err := os.WriteFile(filepath.Join(sparkwingDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSDKFixture(t *testing.T, sparkwingDir, source string) {
	t.Helper()
	writeSDKFixtureModule(t, sparkwingDir)
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("SPARKWING_TEST_DAEMON_ENTRYPOINT") == "1" {
		if err := flag.Set("test.run", "^$"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__sdk-install":
			if err := reuseSDKFixtureCache(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			main()
			return
		case "wingd", "pipeline", "run", "runs", "runners", "overflow", "__tick", "__complete":
			main()
			return
		case "__test-runner":
			os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
			cluster.MainWithVersion(installedVersion())
			return
		}
	}
	code := m.Run()
	for _, directory := range []string{submitCLIDir, sdkFixtureDir} {
		if err := os.RemoveAll(directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	if err := testleak.Check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func TestDetachedHostEntrypointDoesNotRunPipelineFixtures(t *testing.T) {
	for _, args := range [][]string{{"wingd", "run"}, {"pipeline", "describe"}, {"run"}, {"runs"}, {"runners"}, {"overflow"}, {"__sdk-install"}, {"__tick"}, {"__complete"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], append(args, "--help")...)
			cmd.Env = append(os.Environ(), "SPARKWING_TEST_DAEMON_ENTRYPOINT=1")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper entrypoint: %v: %s", err, output)
			}
			if !strings.Contains(string(output), strings.Join(args, " ")) || strings.Contains(string(output), "PASS") {
				t.Fatalf("helper reentered the test suite: %s", output)
			}
		})
	}
}

func TestPersonalRunnerEntrypointDoesNotRunPipelineFixtures(t *testing.T) {
	command := exec.CommandContext(t.Context(), os.Args[0], "__test-runner", "start", "--help")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "sparkwing-runner start") || strings.Contains(string(output), "PASS") {
		t.Fatalf("personal runner helper reentered the test suite: %v\n%s", err, output)
	}
}
