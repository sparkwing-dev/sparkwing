package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseTargetBuildsAllSupportedBinaries(t *testing.T) {
	script, err := filepath.Abs("build-release-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(target+"/"+arch, func(t *testing.T) {
				dir := t.TempDir()
				writeReleaseGoTool(t, dir)
				cmd := exec.Command("bash", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GOOS="+target, "GOARCH="+arch, "TAG=v0.52.4", "CALLS_FILE="+filepath.Join(dir, "calls"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				calls, err := os.ReadFile(filepath.Join(dir, "calls"))
				if err != nil {
					t.Fatal(err)
				}
				if string(calls) != "call\n" {
					t.Fatalf("go calls = %q, want one", calls)
				}
				files, err := os.ReadDir(filepath.Join(dir, "dist"))
				if err != nil {
					t.Fatal(err)
				}
				want := 6
				if target == "windows" {
					want = 2
				}
				if len(files) != want {
					t.Fatalf("outputs=%d want %d", len(files), want)
				}
				for _, file := range files {
					name := file.Name()
					if !strings.Contains(name, "-"+target+"-"+arch) {
						t.Fatal(name)
					}
					if target == "windows" && name != "sparkwing-windows-"+arch+".exe" && name != "sparkwing-runner-windows-"+arch+".exe" {
						t.Fatal(name)
					}
				}
			})
		}
	}
}

func TestReleaseTargetFailsWhenAnyBinaryBuildFails(t *testing.T) {
	script, err := filepath.Abs("build-release-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeReleaseGoTool(t, dir)
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GOOS=linux", "GOARCH=amd64", "TAG=v0.52.4", "CALLS_FILE="+filepath.Join(dir, "calls"), "FAIL_PACKAGE=sparkwing-controller")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 17 {
		t.Fatalf("build succeeded, output: %s", out)
	}
}

func writeReleaseGoTool(t *testing.T, dir string) {
	t.Helper()
	goTool := `#!/bin/sh
set -eu
printf 'call\n' >>"$CALLS_FILE"
out=
packages=
while [ $# -gt 0 ]; do
  if [ "$1" = -o ]; then
    shift
    out="$1"
  fi
  case "$1" in
    ./cmd/*)
      binary="${1##*/}"
      if [ "${FAIL_PACKAGE:-}" = "$binary" ]; then
        exit 17
      fi
      packages="$packages $binary"
      ;;
  esac
  shift
done
[ -n "$out" ]
mkdir -p "$out"
for binary in $packages; do
  ext=
  [ "$GOOS" = windows ] && ext=.exe
  printf '%s' "$GOOS/$GOARCH" >"$out/$binary$ext"
done
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(goTool), 0o700); err != nil {
		t.Fatal(err)
	}
}
