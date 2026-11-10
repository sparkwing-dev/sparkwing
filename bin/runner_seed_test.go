package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerSeedUsesTheSourceSDKPinAndEngineCacheKey(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is unavailable")
	}
	data, err := os.ReadFile("../build/Dockerfile.runner")
	if err != nil {
		t.Fatal(err)
	}
	_, stage, ok := strings.Cut(string(data), "FROM runtime AS go-seed\n")
	if !ok {
		t.Fatal("missing Go seed stage")
	}
	stage, _, _ = strings.Cut(stage, "\nFROM ")
	_, script, ok := strings.Cut(stage, "\nRUN ")
	if !ok {
		t.Fatal("missing seed command")
	}
	root := t.TempDir()
	seed := filepath.Join(root, "seeds")
	script = strings.NewReplacer(
		"/usr/local/share/sparkwing/go-seed", seed,
		"/tmp/engine", filepath.Join(root, "engine"),
		"/tmp/modules", filepath.Join(root, "modules"),
		"/tmp/seed", filepath.Join(root, "seed"),
	).Replace(script)
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
set -eu
[ "$1" = -C ] && shift 2
case "$1 $2" in
  'mod edit') printf '%s\n' '{"Require":[{"Path":"github.com/sparkwing-dev/sparkwing/sparkwing","Version":"v0.1.0"}]}' ;;
  'mod init') : ;;
  'get github.com/sparkwing-dev/sparkwing/sparkwing@v0.1.0') mkdir -p "$GOMODCACHE"; touch "$GOMODCACHE/sdk-v0.1.0" ;;
  'build -trimpath') mkdir -p "$GOCACHE"; touch "$GOCACHE/compiled" ;;
  *) echo "unexpected Go invocation: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SPARKWING_VERSION=v0.72.0", "GOCACHE="+filepath.Join(root, "gocache"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed independent SDK: %v: %s", err, output)
	}
	for _, name := range []string{"modules/sdk-v0.1.0", "gocache/compiled"} {
		if _, err := os.Stat(filepath.Join(seed, "v0.72.0", name)); err != nil {
			t.Fatal(err)
		}
	}
}
