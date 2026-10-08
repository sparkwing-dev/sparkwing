package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWebBuildLockRuntimeAndInheritance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, tool := range []string{"bash", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	root := filepath.Join(t.TempDir(), "checkout with spaces %WINDIR% 'quote")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("web-build-lock.sh")
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "entry with spaces.sh")
	script := `#!/usr/bin/env bash
set -euo pipefail
source "$1"
root="$2"
if [[ "${SPARKWING_WEB_BUILD_LOCK_REEXEC:-0}" != 1 ]]; then
  export WEB_TEST_DYNAMIC_EXPORT='value with spaces'
  export GOPRIVATE=example.invalid/private
  export SPARKWING_INSTALL_BIN="$root/install with spaces"
fi
if [[ "${5:-}" == second && "${SPARKWING_WEB_BUILD_LOCK_REEXEC:-0}" != 1 ]]; then
  if flock -n "$root/internal/web/.build-state/lock" true; then exit 1; fi
  echo attempting
fi
if [[ "${5:-}" == stale || "${6:-}" == stale ]]; then
  mkdir -p "$root/internal/web/.build-state"
  exec 9>"$root/internal/web/.build-state/lock"
  export SPARKWING_WEB_BUILD_LOCK_FD=9
fi
sparkwing_lock_web_build "$root" "$@"
[[ "$3" == 'argument with spaces' ]]
[[ "$WEB_TEST_DYNAMIC_EXPORT" == 'value with spaces' ]]
[[ "$GOPRIVATE" == example.invalid/private ]]
[[ "$SPARKWING_INSTALL_BIN" == "$root/install with spaces" ]]
[[ "$SPARKWING_WEB_BUILD_LOCKED" == 1 ]]
if flock -n "$root/internal/web/.build-state/lock" true; then
  echo 'lock was not held' >&2
  exit 1
fi
"$BASH" -c 'set -euo pipefail; source "$1"; sparkwing_lock_web_build "$2"; [[ "$SPARKWING_WEB_BUILD_LOCKED" == 1 ]]' -- "$1" "$root"
if flock -n "$root/internal/web/.build-state/lock" true; then
  echo 'nested child released parent lock' >&2
  exit 1
fi
if [[ -n "${4:-}" && "$4" != - ]]; then
  printf 'start-%s\n' "$5" >> "$4"
  echo acquired
  IFS= read -r release
  printf 'end-%s\n' "$5" >> "$4"
fi
echo inherited-lock-ok
`
	if err := os.WriteFile(entry, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, err := os.Stat(bash); err != nil {
			t.Skip("Git Bash unavailable")
		}
	}
	cmd := exec.CommandContext(ctx, bash, filepath.ToSlash(entry), filepath.ToSlash(helper), filepath.ToSlash(root), "argument with spaces")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lock: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "inherited-lock-ok") {
		t.Fatalf("missing inherited lock evidence: %s", out)
	}
	stale := exec.CommandContext(ctx, bash, filepath.ToSlash(entry), filepath.ToSlash(helper), filepath.ToSlash(root), "argument with spaces", "-", "stale")
	if out, err := stale.CombinedOutput(); err != nil {
		t.Fatalf("unheld inherited descriptor: %v\n%s", err, out)
	}
	trace := filepath.ToSlash(filepath.Join(root, "trace"))
	startHolder := func(name string) (*exec.Cmd, *bufio.Scanner, func()) {
		t.Helper()
		cmd := exec.CommandContext(ctx, bash, filepath.ToSlash(entry), filepath.ToSlash(helper), filepath.ToSlash(root), "argument with spaces", trace, name, "stale")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stopClosing := context.AfterFunc(ctx, func() {
			_ = stdin.Close()
			_ = stdout.Close()
		})
		t.Cleanup(func() { stopClosing(); _ = stdin.Close(); _ = stdout.Close() })
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		return cmd, bufio.NewScanner(stdout), func() { _, _ = stdin.Write([]byte("release\n")); _ = stdin.Close() }
	}
	awaitSignal := func(scanner *bufio.Scanner, signal string) {
		t.Helper()
		for scanner.Scan() {
			if scanner.Text() == signal {
				return
			}
		}
		t.Fatalf("missing %s signal: %v", signal, scanner.Err())
	}
	first, firstOutput, releaseFirst := startHolder("first")
	awaitSignal(firstOutput, "acquired")
	second, secondOutput, releaseSecond := startHolder("second")
	awaitSignal(secondOutput, "attempting")
	releaseFirst()
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	awaitSignal(secondOutput, "acquired")
	releaseSecond()
	if err := second.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "start-first\nend-first\nstart-second\nend-second\n" {
		t.Fatalf("overlapping lock holders: %q", data)
	}
}
