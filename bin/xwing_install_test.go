package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateInstallUsesSelectedSourceAndPrivateDestination(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	source := filepath.Join(fixture, "selected checkout")
	stub := filepath.Join(fixture, "stub")
	for _, dir := range []string{filepath.Join(source, "bin"), stub} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyScript := func(name string) {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(root, "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "bin", name), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyScript("xwing-install.sh")
	copyScript("install.sh")
	copyScript("web-build-lock.sh")
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture"}, {"tag", "v0.1.0"}} {
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	web := "#!/usr/bin/env bash\nset -euo pipefail\nprintf rebuilt > \"$XWING_TOOL_SOURCE/web-rebuilt\"\n"
	if err := os.WriteFile(filepath.Join(source, "bin", "build-web.sh"), []byte(web), 0o755); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(fixture, "compiler-trace")
	compiler := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$GOWORK" "$@" > "$CANDIDATE_TRACE"
if command -v flock >/dev/null 2>&1; then
 if flock -n "$XWING_TOOL_SOURCE/internal/web/.build-state/lock" true; then
  echo "compiler reached an unlocked export" >&2
  exit 27
 fi
fi
if [[ "${CANDIDATE_FAIL:-}" == 1 ]]; then exit 23; fi
out=""
while (( $# )); do
 if [[ "$1" == -o ]]; then shift; out="$1"; fi
 shift
done
[[ -n "$out" ]] || exit 24
printf '#!/bin/sh\nexit 0\n' > "$out"
chmod +x "$out"
`
	if err := os.WriteFile(filepath.Join(stub, "go"), []byte(compiler), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CANDIDATE_TRACE", trace)
	t.Setenv("XWING_TOOL_SOURCE", source)
	t.Setenv("SKIP_WEB_BUILD", "1")
	t.Setenv("GOWORK", filepath.Join(fixture, "unusable.go.work"))
	dest := filepath.Join(fixture, "candidate")
	t.Setenv("XWING_TOOL_DEST", dest)
	run := func() ([]byte, error) {
		return exec.Command("bash", filepath.Join(source, "bin", "xwing-install.sh")).CombinedOutput()
	}
	if out, err := run(); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if info, err := os.Stat(dest); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("candidate executable: %v", err)
	}
	content, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "off\n-C\n"+filepath.Join(source, "")+"\nbuild\n") {
		t.Fatalf("compiler arguments = %s", content)
	}
	if content, err := os.ReadFile(filepath.Join(source, "web-rebuilt")); err != nil || string(content) != "rebuilt" {
		t.Fatalf("ambient SKIP_WEB_BUILD bypassed native web build: %v", err)
	}
	if !strings.Contains(string(content), "-trimpath\n-ldflags\n-s -w -X main.Version=v0.1.0-dev+") {
		t.Fatalf("native version recipe missing: %s", content)
	}
	if out, err := run(); err == nil || !strings.Contains(string(out), "already exists") {
		t.Fatalf("existing target accepted: %v %s", err, out)
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDIDATE_FAIL", "1")
	if out, err := run(); err == nil {
		t.Fatalf("compiler failure hidden: %s", out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("failed build published destination: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(fixture, ".sparkwing-build.*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("left staging directories: %v %v", stages, err)
	}
	t.Setenv("XWING_TOOL_SOURCE", fixture)
	if out, err := run(); err == nil || !strings.Contains(string(out), "does not own") {
		t.Fatalf("wrong source accepted: %v %s", err, out)
	}
}
