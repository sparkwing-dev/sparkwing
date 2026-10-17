package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
)

func TestResolveSparksToolchainFailureHasFixWithoutNoUpdateHint(t *testing.T) {
	dir := newSparksFixture(t, "libraries:\n  - name: lib\n    source: example.com/lib\n    version: v1.0.0\n")
	bin := filepath.Join(t.TempDir(), "go")
	script := `#!/bin/sh
if [ "$1" = env ]; then printf 'local\ngo1.26.6\noff\n'; exit 0; fi
printf 'go: example.com/lib requires go >= 1.26.8 (running go 1.26.6; GOTOOLCHAIN=local)\n' >&2
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SPARKS_GO_BIN", bin)
	t.Setenv("GOTOOLCHAIN", "local")
	err := resolveSparks(context.Background(), dir, compileOptions{})
	var floorError *gotoolchain.Error
	if !errors.As(err, &floorError) {
		t.Fatalf("error = %v, want toolchain error", err)
	}
	for _, want := range []string{"1.26.8", "1.26.6", "GOTOOLCHAIN environment variable", "unset GOTOOLCHAIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "--sw-no-update") {
		t.Fatalf("misleading hint: %v", err)
	}
}

func TestPipelineToolchainOverrideStaysInTheBuild(t *testing.T) {
	// safety: the pin is process state, so the build runs in a child that
	// checks its own GOTOOLCHAIN afterwards.
	if dir := os.Getenv("SPARKWING_TEST_TOOLCHAIN_CHILD_DIR"); dir != "" {
		uncached := os.Getenv("SPARKWING_TEST_TOOLCHAIN_UNCACHED") == "true"
		if err := materializeOnly(dir, os.Environ(), compileOptions{NoUpdate: true, NoBincache: uncached}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if os.Getenv("GOTOOLCHAIN") != "go1.26.6" {
			fmt.Fprintln(os.Stderr, "process pin changed")
			os.Exit(1)
		}
		os.Exit(0)
	}
	for _, uncached := range []bool{false, true} {
		name := "cached"
		if uncached {
			name = "uncached"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".sparkwing")
			bin := filepath.Join(root, "bin")
			for _, path := range []string{dir, bin} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/pipeline\ngo 1.26.8\n")
			writeFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main() {}\n")
			probeLog := filepath.Join(root, "probes")
			prepareLog := filepath.Join(root, "prepare")
			buildLog := filepath.Join(root, "build")
			script := `#!/bin/sh
if [ "$1" = env ]; then
 printf 'probe\n' >> "$PROBE_LOG"
 printf 'go1.26.6\ngo1.26.6\noff\n'
 exit 0
fi
if [ "$1" = list ]; then printf '%s\n' "$GOTOOLCHAIN" >> "$PREPARE_LOG"; exit 0; fi
printf '%s\n' "$GOTOOLCHAIN" >> "$BUILD_LOG"
while [ $# -gt 0 ]; do
 if [ "$1" = -o ]; then
  shift
  cat > "$1" <<'PIPELINE'
#!/bin/sh
if [ "$1" = --sw-node-protocol ]; then read -r _ <&3; printf '{"reply":"1","ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"fixture"},"features":[]}}\n' >&4; read -r _ <&3; printf '{"reply":"2","ok":true,"result":{"pipelines":[]}}\n' >&4; exit 0; fi
PIPELINE
  chmod +x "$1"
  exit 0
 fi
 shift
done
exit 1
`
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
			t.Setenv("GOTOOLCHAIN", "go1.26.6")
			t.Setenv("PROBE_LOG", probeLog)
			t.Setenv("PREPARE_LOG", prepareLog)
			t.Setenv("BUILD_LOG", buildLog)
			t.Setenv("SPARKWING_GITCACHE_URL", "")
			child := exec.Command(os.Args[0], "-test.run=^TestPipelineToolchainOverrideStaysInTheBuild$")
			child.Env = append(os.Environ(), "SPARKWING_TEST_TOOLCHAIN_CHILD_DIR="+dir,
				fmt.Sprintf("SPARKWING_TEST_TOOLCHAIN_UNCACHED=%t", uncached))
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("launch: %v: %s", err, out)
			}
			prepared, err := os.ReadFile(prepareLog)
			if uncached {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("uncached preparation = %q, %v", prepared, err)
				}
			} else if err != nil || strings.TrimSpace(string(prepared)) != "go1.26.8" {
				t.Fatalf("cached preparation = %q, %v", prepared, err)
			}
			for path, want := range map[string]string{buildLog: "go1.26.8", probeLog: "probe"} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(data)) != want {
					t.Fatalf("%s = %q, want %q", filepath.Base(path), data, want)
				}
			}
		})
	}
}
