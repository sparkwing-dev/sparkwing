package main

import (
	"context"
	"errors"
	"os"
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

func TestPipelineToolchainOverrideDoesNotReachRuntime(t *testing.T) {
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
			buildLog := filepath.Join(root, "build")
			runtimeLog := filepath.Join(root, "runtime")
			script := `#!/bin/sh
if [ "$1" = env ]; then
 printf 'probe\n' >> "$PROBE_LOG"
 printf 'go1.26.6\ngo1.26.6\noff\n'
 exit 0
fi
printf '%s\n' "$GOTOOLCHAIN" >> "$BUILD_LOG"
while [ $# -gt 0 ]; do
 if [ "$1" = -o ]; then
  shift
  cat > "$1" <<'PIPELINE'
#!/bin/sh
if [ "$1" = --describe ]; then printf '[]\n'; exit 0; fi
printf '%s\n' "$GOTOOLCHAIN" > "$RUNTIME_LOG"
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
			t.Setenv("BUILD_LOG", buildLog)
			t.Setenv("RUNTIME_LOG", runtimeLog)
			t.Setenv("SPARKWING_FLEET", "1")
			t.Setenv("SPARKWING_GITCACHE_URL", "")
			runtimeEnv := os.Environ()
			if err := compileAndExec(dir, nil, runtimeEnv, compileOptions{NoUpdate: true, NoBincache: uncached}); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]string{buildLog: "go1.26.8", runtimeLog: "go1.26.6", probeLog: "probe"} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(data)) != want {
					t.Fatalf("%s = %q, want %q", filepath.Base(path), data, want)
				}
			}
			if os.Getenv("GOTOOLCHAIN") != "go1.26.6" {
				t.Fatal("process pin changed")
			}
		})
	}
}
