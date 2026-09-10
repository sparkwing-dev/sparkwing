package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type webBuildFixture struct {
	root, trace string
	env         []string
}

func newWebBuildFixture(t *testing.T) webBuildFixture {
	t.Helper()
	for _, tool := range []string{"node", "flock", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	stub := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "web", "src")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"build-web.sh", "web-build-lock.sh", "web-build-proof.mjs"} {
		data, err := os.ReadFile(name)
		if os.IsNotExist(err) && name != "build-web.sh" {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "bin", name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	trace := filepath.Join(t.TempDir(), "calls")
	npm := `#!/bin/sh
set -eu
case "$1" in
 --version) printf '%s\n' "${WEB_TEST_NPM_VERSION:-10.9.8}" ;;
 config) if [ -n "${WEB_TEST_NPM_CONFIG:-}" ]; then printf '%s\n' "$WEB_TEST_NPM_CONFIG"; else printf '{}\n'; fi ;;
 ci)
  case " $* " in *" --include=dev "*) ;; *) echo 'build dependencies omitted' >&2; exit 26 ;; esac
  printf 'ci\n' >> "$WEB_TEST_TRACE" ;;

 run)
  [ "$2" = build ]
  printf 'build\n' >> "$WEB_TEST_TRACE"
  if [ "${WEB_TEST_DELAY:-}" = 1 ]; then sleep 1; fi
  if [ "${WEB_TEST_FAIL:-}" = 1 ]; then exit 23; fi
  mkdir -p out
  cat src/page.tsx > out/index.html
  printf '%s' "${NEXT_PUBLIC_API_TOKEN:-asset}" > out/app.js
  if [ "${WEB_TEST_MUTATE:-}" = 1 ]; then printf changed >> src/page.tsx; fi
  ;;
 *) exit 24 ;;
esac
`
	if err := os.WriteFile(filepath.Join(stub, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	f := webBuildFixture{root: root, trace: trace, env: append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "WEB_TEST_TRACE="+trace, "NODE_ENV=production")}
	f.write(t, "web/src/page.tsx", "first")
	f.write(t, "web/package.json", `{"scripts":{"build":"next build"}}`)
	f.write(t, "web/package-lock.json", `{"lockfileVersion":3}`)
	return f
}

func (f webBuildFixture) write(t *testing.T, path, body string) {
	t.Helper()
	p := filepath.Join(f.root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f webBuildFixture) run(t *testing.T, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(f.root, "bin", "build-web.sh"), "--reuse")
	cmd.Env = append(append([]string{}, f.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f webBuildFixture) mustRun(t *testing.T, extraEnv ...string) {
	t.Helper()
	if out, err := f.run(t, extraEnv...); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
}

func (f webBuildFixture) builds(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "build\n")
}

func TestWebBuildReuseSkipsUnchangedFrontendForGoEdits(t *testing.T) {
	f := newWebBuildFixture(t)
	f.mustRun(t)
	info, err := os.Stat(filepath.Join(f.root, "internal/web/next-out/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	f.mustRun(t)
	f.write(t, "cmd/sparkwing/example.go", "package main\n")
	f.mustRun(t)
	if got := f.builds(t); got != 1 {
		t.Fatalf("unchanged frontend rebuilt %d times; want one", got)
	}
	after, err := os.Stat(filepath.Join(f.root, "internal/web/next-out/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("reuse rewrote the existing export")
	}
}

func TestWebBuildReuseInvalidatesContentAndBuildInputs(t *testing.T) {
	cases := map[string]func(*testing.T, webBuildFixture) []string{
		"source bytes with preserved mtime": func(t *testing.T, f webBuildFixture) []string {
			p := filepath.Join(f.root, "web/src/page.tsx")
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			f.write(t, "web/src/page.tsx", "second")
			if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			return nil
		},
		"nested build source": func(t *testing.T, f webBuildFixture) []string {
			f.write(t, "web/src/build/component.tsx", "export const ready = true")
			return nil
		},
		"effective npm config": func(t *testing.T, f webBuildFixture) []string {
			return []string{`WEB_TEST_NPM_CONFIG={"omit":["dev"]}`}
		},
		"untracked source addition": func(t *testing.T, f webBuildFixture) []string {
			f.write(t, "web/src/new.ts", "export const fresh = true")
			return nil
		},
		"source removal": func(t *testing.T, f webBuildFixture) []string {
			if err := os.Remove(filepath.Join(f.root, "web/unused.ts")); err != nil {
				t.Fatal(err)
			}
			return nil
		},
		"lockfile": func(t *testing.T, f webBuildFixture) []string {
			f.write(t, "web/package-lock.json", `{"lockfileVersion":3,"changed":true}`)
			return nil
		},
		"configuration": func(t *testing.T, f webBuildFixture) []string {
			f.write(t, "web/next.config.ts", "export default { output: 'export' }")
			return nil
		},
		"ignored dotenv": func(t *testing.T, f webBuildFixture) []string {
			f.write(t, "web/.env.production", "NEXT_PUBLIC_LABEL=changed\n")
			return nil
		},
		"public environment": func(t *testing.T, f webBuildFixture) []string {
			return []string{"NEXT_PUBLIC_API_TOKEN=fixture-private-value"}
		},
		"npm identity": func(t *testing.T, f webBuildFixture) []string { return []string{"WEB_TEST_NPM_VERSION=99.0.0"} },
		"builder recipe": func(t *testing.T, f webBuildFixture) []string {
			p := filepath.Join(f.root, "bin/build-web.sh")
			body, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, append(body, []byte("\n# Updated build recipe.\n")...), 0o755); err != nil {
				t.Fatal(err)
			}
			return nil
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWebBuildFixture(t)
			f.write(t, "web/unused.ts", "export {}")
			f.mustRun(t)
			f.mustRun(t, change(t, f)...)
			if got := f.builds(t); got != 2 {
				t.Fatalf("changed input caused %d builds, want two", got)
			}
		})
	}
}

func TestWebBuildReuseValidatesCompleteExport(t *testing.T) {
	for _, mode := range []string{"missing asset", "edited asset", "missing index", "extra asset"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebBuildFixture(t)
			f.mustRun(t)
			output := filepath.Join(f.root, "internal/web/next-out")
			switch mode {
			case "missing asset":
				if err := os.Remove(filepath.Join(output, "app.js")); err != nil {
					t.Fatal(err)
				}
			case "edited asset":
				f.write(t, "internal/web/next-out/app.js", "corrupt")
			case "missing index":
				if err := os.Remove(filepath.Join(output, "index.html")); err != nil {
					t.Fatal(err)
				}
			case "extra asset":
				f.write(t, "internal/web/next-out/extra.js", "unrecorded")
			}
			f.mustRun(t)
			if got := f.builds(t); got != 2 {
				t.Fatalf("damaged export caused %d builds, want two", got)
			}
		})
	}
}

func TestWebBuildFailuresCannotLeaveReusableProof(t *testing.T) {
	for _, mode := range []string{"failed build", "inputs changed during build"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebBuildFixture(t)
			f.mustRun(t)
			f.write(t, "web/src/page.tsx", "changed")
			setting := "WEB_TEST_FAIL=1"
			if mode != "failed build" {
				setting = "WEB_TEST_MUTATE=1"
			}
			if out, err := f.run(t, setting); err == nil {
				t.Fatalf("unsafe build succeeded: %s", out)
			}
			f.mustRun(t)
			if got := f.builds(t); got != 3 {
				t.Fatalf("failure allowed reuse: %d builds", got)
			}
		})
	}
}

func TestWebBuildProofDoesNotStoreEnvironmentValues(t *testing.T) {
	f := newWebBuildFixture(t)
	f.mustRun(t, "NEXT_PUBLIC_API_TOKEN=fixture-private-value")
	data, err := os.ReadFile(filepath.Join(f.root, "internal/web/.build-state/receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fixture-private-value") {
		t.Fatal("receipt persisted an environment value")
	}
}

func TestWebBuildSerializesConcurrentReuse(t *testing.T) {
	f := newWebBuildFixture(t)
	log, err := os.CreateTemp(t.TempDir(), "first-build")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	}()
	first := exec.Command("bash", filepath.Join(f.root, "bin/build-web.sh"), "--reuse")
	first.Env = append(append([]string{}, f.env...), "WEB_TEST_DELAY=1")
	first.Stdout, first.Stderr = log, log
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			if err := first.Wait(); err != nil {
				t.Error(err)
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, readErr := os.ReadFile(f.trace)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "build\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first build did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mustRun(t)
	if err := first.Wait(); err != nil {
		t.Fatalf("first build: %v", err)
	}
	waited = true
	if got := f.builds(t); got != 1 {
		t.Fatalf("concurrent reuse launched %d builds", got)
	}
}

func TestWebBuildNormalizesProductionAndRebuildsWithoutReuse(t *testing.T) {
	f := newWebBuildFixture(t)
	f.mustRun(t)
	f.mustRun(t, "NODE_ENV=development")
	if got := f.builds(t); got != 1 {
		t.Fatalf("equivalent production settings caused %d builds", got)
	}
	cmd := exec.Command("bash", filepath.Join(f.root, "bin/build-web.sh"))
	cmd.Env = f.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("default build: %v %s", err, out)
	}
	if got := f.builds(t); got != 2 {
		t.Fatalf("ordinary build reused instead of building: %d", got)
	}
}

func TestWebBuildCustomNodeConfigurationFallsBackToBuild(t *testing.T) {
	for _, config := range []string{"NODE_OPTIONS=--max-old-space-size=256", `WEB_TEST_NPM_CONFIG={"node-options":"--max-old-space-size=256"}`, `WEB_TEST_NPM_CONFIG={"script-shell":"/bin/sh"}`} {
		t.Run(config, func(t *testing.T) {
			f := newWebBuildFixture(t)
			f.mustRun(t, config)
			f.mustRun(t, config)
			if got := f.builds(t); got != 2 {
				t.Fatalf("custom execution configuration reused an unproven build: %d", got)
			}
		})
	}
}

func TestWebBuildWithoutFlockBuildsFresh(t *testing.T) {
	f := newWebBuildFixture(t)
	isolated := t.TempDir()
	for _, tool := range []string{"bash", "dirname", "mkdir", "chmod", "rm", "cp", "touch", "cat", "sleep", "node"} {
		binary, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(binary, filepath.Join(isolated, tool)); err != nil {
			t.Fatal(err)
		}
	}
	var originalStub string
	for _, value := range f.env {
		if strings.HasPrefix(value, "PATH=") {
			originalStub = filepath.SplitList(strings.TrimPrefix(value, "PATH="))[0]
		}
	}
	if err := os.Symlink(filepath.Join(originalStub, "npm"), filepath.Join(isolated, "npm")); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, "PATH="+isolated)
	f.mustRun(t, "PATH="+isolated)
	if got := f.builds(t); got != 2 {
		t.Fatalf("uncoordinated build reused output: %d", got)
	}
}
