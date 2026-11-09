package jobs

import (
	"archive/zip"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestOwnedSDKUpdatesInAnEnvironmentAndUsesTheReposLandFlow(t *testing.T) {
	for _, mode := range []string{"land", "dry-run", "failed-land", "current"} {
		t.Run(mode, func(t *testing.T) {
			version := "v0.0.0-20261101000000-0123456789ab"
			const sha = "0123456789abcdef0123456789abcdef01234567"
			repo := atomicReleaseRepo(t, fixtureChangelog)
			mod := filepath.Join(repo, ".sparkwing")
			if err := os.MkdirAll(mod, 0o755); err != nil {
				t.Fatal(err)
			}
			require := "require " + sdkModulePath + " v0.1.0\nreplace " + sdkModulePath + " v0.1.0 => ../old-sdk\n"
			if mode == "current" {
				require = "require " + sdkModulePath + " " + version + "\nreplace " + sdkModulePath + " => " + sdkModulePath + " " + version + "\n"
			}
			writeFile(t, filepath.Join(mod, "go.mod"), "module fixture\n\ngo 1.26.0\n\n"+require)
			writeFile(t, filepath.Join(mod, "go.sum"), "")
			writeFile(t, filepath.Join(mod, "main.go"), "package main\nimport _ \""+sdkModulePath+"\"\nfunc main() {}\n")
			gitRun(t, repo, "add", ".sparkwing")
			gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
				"-c", "core.hooksPath=/dev/null", "commit", "-m", "pipeline fixture")
			before := gitRun(t, repo, "rev-parse", "HEAD")
			candidate := filepath.Join(t.TempDir(), "candidate")
			gitRun(t, repo, "clone", "--", repo, candidate)
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "xwing.log")
			attached, _ := json.Marshal(struct{ Path string }{candidate})
			t.Setenv("OWNED_TEST_ATTACHED", string(attached))
			t.Setenv("OWNED_TEST_LOG", log)
			t.Setenv("OWNED_TEST_MODE", mode)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			writeDeliveryTool(t, filepath.Join(bin, "xwing"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$OWNED_TEST_LOG"
case "$2" in
  add) printf '%s\n' "$OWNED_TEST_ATTACHED" ;;
  land) [ "$OWNED_TEST_MODE" != failed-land ] ;;
esac
`)
			for key, value := range map[string]string{
				"GIT_CONFIG_COUNT": "4",
				"GIT_CONFIG_KEY_0": "user.name", "GIT_CONFIG_VALUE_0": "Test",
				"GIT_CONFIG_KEY_1": "user.email", "GIT_CONFIG_VALUE_1": "test@example.invalid",
				"GIT_CONFIG_KEY_2": "commit.gpgsign", "GIT_CONFIG_VALUE_2": "false",
				"GIT_CONFIG_KEY_3": "core.hooksPath", "GIT_CONFIG_VALUE_3": "/dev/null",
			} {
				t.Setenv(key, value)
			}
			ownedSDKProxy(t, version)
			ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
			err := updateOwnedSDK(ctx, repo, "fixture", version, sha, mode == "dry-run")
			if (err != nil) != (mode == "failed-land") {
				t.Fatalf("update = %v", err)
			}
			if mode == "failed-land" {
				beforeRetry := gitRun(t, candidate, "rev-parse", "HEAD")
				t.Setenv("OWNED_TEST_MODE", "land")
				if err := updateOwnedSDK(ctx, repo, "fixture", version, sha, false); err != nil {
					t.Fatalf("retry land: %v", err)
				}
				if afterRetry := gitRun(t, candidate, "rev-parse", "HEAD"); afterRetry != beforeRetry {
					t.Fatal("retry created another pin commit")
				}
			}
			calls, readErr := os.ReadFile(log)
			if mode == "failed-land" && strings.Count(string(calls), "env land ") != 2 {
				t.Fatalf("landing was not retried: %s", calls)
			}
			if mode == "current" || mode == "dry-run" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("preview or unchanged pin invoked Xwing: %s", calls)
				}
			} else {
				if !strings.Contains(string(calls), "env land --session sdk-main-fixture-"+sha[:12]+" --repo fixture") {
					t.Fatalf("update did not use the repo landing flow: %s", calls)
				}
				pin, err := os.ReadFile(filepath.Join(candidate, ".sparkwing", "go.mod"))
				if err != nil {
					t.Fatal(err)
				}
				if current, err := ownedSDKCurrent(pin, version); err != nil || !current || strings.Contains(string(pin), "../old-sdk") {
					t.Fatalf("wrong main pin: %s (%v)", pin, err)
				}
				if status := gitRun(t, candidate, "status", "--porcelain"); status != "" {
					t.Fatalf("update was not committed: %s", status)
				}
				if changed := strings.Fields(gitRun(t, candidate, "diff", "--name-only", "HEAD^", "HEAD")); len(changed) != 2 {
					t.Fatalf("commit touched unexpected files: %v", changed)
				}
			}
			if after := gitRun(t, repo, "rev-parse", "HEAD"); after != before {
				t.Fatal("updater modified source checkout outside its land flow")
			}
		})
	}
}

func ownedSDKProxy(t *testing.T, version string) {
	t.Helper()
	base := t.TempDir()
	proxy := filepath.Join(base, filepath.FromSlash(sdkModulePath), "@v")
	if err := os.MkdirAll(proxy, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := "module " + sdkModulePath + "\n\ngo 1.26.0\n"
	writeFile(t, filepath.Join(proxy, version+".info"), `{"Version":"`+version+`","Time":"2026-11-01T00:00:00Z"}`)
	writeFile(t, filepath.Join(proxy, version+".mod"), mod)
	file, err := os.Create(filepath.Join(proxy, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for path, body := range map[string]string{"go.mod": mod, "sdk.go": "package sparkwing\n"} {
		entry, err := writer.Create(sdkModulePath + "@" + version + "/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", (&url.URL{Scheme: "file", Path: base}).String())
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-modcacherw -p=1")
}

func TestOwnedSDKOnlySelectsOwnedGoReposWithAMainLandPolicy(t *testing.T) {
	repo := atomicReleaseRepo(t, fixtureChangelog)
	gitRun(t, repo, "remote", "add", "origin", "https://github.com/koreyGambill/fixture")
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".sparkwing", "go.mod"), "module fixture\n")
	for _, tc := range []struct {
		origin, policy string
		want           bool
	}{
		{"git@github.com:koreyGambill/fixture.git", "landing: {branch: main, verify: verify}", true},
		{"https://github.com/sparkwing-dev/fixture", "landing: {branch: main, verify: verify}", true},
		{"https://github.com/employer/fixture", "landing: {branch: main, verify: verify}", false},
		{"https://github.com/koreyGambill/fixture", "landing: {branch: main, verify: verify, delivery: pull-request}", false},
		{"https://github.com/koreyGambill/fixture", "landing: {branch: develop, verify: verify}", false},
		{"https://github.com/koreyGambill/fixture", "landing: {branch: main}", false},
	} {
		writeFile(t, filepath.Join(repo, ".xwing-env.yaml"), tc.policy)
		gitRun(t, repo, "remote", "set-url", "origin", tc.origin)
		ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
		if got := ownedGoRepo(ctx, repo); got != tc.want {
			origin, err := runGitIn(ctx, repo, "remote", "get-url", "origin")
			t.Fatalf("selected %s with %s = %v, want %v (origin %q, error %v, match %v)", tc.origin, tc.policy, got, tc.want, origin, err, ownedOrigin.MatchString(strings.TrimSpace(origin)))
		}
	}
}

func TestOwnedSDKCurrentUsesTheEffectiveReplacement(t *testing.T) {
	const version = "v0.9.0"
	for _, tc := range []struct {
		replace string
		want    bool
	}{
		{"replace " + sdkModulePath + " v0.8.0 => " + sdkModulePath + " " + version, false},
		{"replace " + sdkModulePath + " " + version + " => " + sdkModulePath + " " + version, true},
		{"replace " + sdkModulePath + " => " + sdkModulePath + " " + version, true},
		{"replace " + sdkModulePath + " => " + sdkModulePath + " " + version + "\nreplace " + sdkModulePath + " " + version + " => ../wrong", false},
	} {
		got, err := ownedSDKCurrent([]byte("module fixture\nrequire "+sdkModulePath+" "+version+"\n"+tc.replace+"\n"), version)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %v, %v; want %v", tc.replace, got, err, tc.want)
		}
	}
}
