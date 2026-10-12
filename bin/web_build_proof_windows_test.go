package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebBuildProofWindowsPNPMCommand(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	stub := filepath.Join(t.TempDir(), "tools with spaces %WINDIR% !literal!")
	for _, dir := range []string{"bin", "web", "internal/web/next-out", "internal/web/.build-state"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build-web.sh", "web-build-lock.sh", "web-build-proof.mjs"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "bin", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"web/pnpm.cmd": "@exit /b 25\r\n", "web/package.json": "{}", "web/page.js": "source", "internal/web/next-out/index.html": "output"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pnpm := "@echo off\r\nif \"%WEB_TEST_PNPM_FAILURE%\"==\"1\" (echo private-diagnostic-secret 1>&2 & exit /b 23)\r\nif \"%1\"==\"--version\" (echo 12.5.1 & exit /b 0)\r\nif \"%1\"==\"config\" (echo {\"registry\":\"%WEB_TEST_REGISTRY%\"} & exit /b 0)\r\nexit /b 24\r\n"
	if err := os.WriteFile(filepath.Join(stub, "pnpm.cmd"), []byte(pnpm), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(action, digest string, extra ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(node, filepath.Join(root, "bin", "web-build-proof.mjs"), action, root, digest)
		relativeStub, err := filepath.Rel(root, stub)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+relativeStub+string(os.PathListSeparator)+os.Getenv("PATH"), "NODE_ENV=production", "NODE_OPTIONS=", "NODE_PATH=", "WEB_TEST_REGISTRY=first")
		cmd.Env = append(cmd.Env, extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	input, err := run("inputs", "")
	if err != nil {
		t.Fatalf("input proof: %v\n%s", err, input)
	}
	digest := strings.TrimSpace(input)
	if len(digest) != 64 {
		t.Fatalf("invalid input digest %q", digest)
	}
	if out, err := run("record", digest); err != nil {
		t.Fatalf("record: %v\n%s", err, out)
	}
	if out, err := run("check", ""); err != nil {
		t.Fatalf("reuse: %v\n%s", err, out)
	}
	if out, err := run("check", "", "WEB_TEST_REGISTRY=changed"); err == nil {
		t.Fatalf("changed pnpm config reused proof: %s", out)
	}
	out, err := run("inputs", "", "WEB_TEST_PNPM_FAILURE=1")
	if err == nil || strings.Contains(out, "private-diagnostic-secret") || !strings.Contains(out, "proof unavailable") {
		t.Fatalf("failed pnpm diagnostics: error=%v output=%q", err, out)
	}
}
