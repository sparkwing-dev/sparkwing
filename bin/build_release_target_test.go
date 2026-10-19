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
				initReleaseRepo(t, dir)
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
				want := 3
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
	initReleaseRepo(t, dir)
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
if [ "$1" = version ]; then
  printf '%s: go1.26.9\n\tmod\tgithub.com/sparkwing-dev/sparkwing\t%s\n' "$3" "${STAMPED_VERSION-$TAG}"
  exit 0
fi
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

func TestReleaseTargetRefusesABinaryThatDoesNotReportItsVersion(t *testing.T) {
	script, err := filepath.Abs("build-release-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]struct {
		env []string
		tag string
	}{
		"unstamped":     {env: []string{"STAMPED_VERSION=(devel)"}},
		"tag elsewhere": {tag: "v0.52.4"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeReleaseGoTool(t, dir)
			initReleaseRepo(t, dir)
			if setup.tag != "" {
				gitIn(t, dir, "tag", setup.tag)
				gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "next")
			}
			cmd := exec.Command("bash", script)
			cmd.Dir = dir
			cmd.Env = append(append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GOOS=linux", "GOARCH=amd64", "TAG=v0.52.4", "CALLS_FILE="+filepath.Join(dir, "calls")), setup.env...)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("release build accepted it: %s", out)
			}
			if _, err := os.Stat(filepath.Join(dir, "dist", "sparkwing-linux-amd64")); err == nil {
				t.Fatal("release build published an asset")
			}
		})
	}
}

func initReleaseRepo(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "release")
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
