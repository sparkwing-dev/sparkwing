//go:build !windows

package nodehost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestCacheDirectoryOnASharedHostNeverRunsTheJobsTools(t *testing.T) {
	for _, resolver := range []string{"go-modules", "npm"} {
		t.Run(resolver, func(t *testing.T) {
			input, state, _, _ := executorFixture(t)
			input.Entry = acquiredLayerEntry(t, 1)
			workspace := input.Entry.CheckoutDirectory()
			tools := filepath.Join(workspace, "tools")
			if err := os.Mkdir(tools, 0o755); err != nil {
				t.Fatal(err)
			}
			name := "go"
			if resolver == "npm" {
				name = "npm"
			}
			script := "#!/bin/sh\n[ -z \"$SPARKWING_CLAIM_TOKEN\" ] || exit 7\n[ \"$RESOLVER_INPUT\" = sealed ] || exit 8\nprintf '%s/cache' \"$PWD\"\n"
			if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			job := nodewire.JobSpec{ID: input.NodeID, Env: map[string]nodewire.EnvValue{"PATH": {Value: "tools"}, "RESOLVER_INPUT": {Value: "sealed"}}}
			state.row.Spec, _ = json.Marshal(job)
			environments := SDKEnvironment{Store: state, Base: procenv.Base{Ambient: []string{"PATH=/nonexistent", "HOME=/nonexistent"}, Masker: secrets.NewMasker()}}
			dir, err := environments.cacheDirectory(t.Context(), input, nodewire.CacheSpec{Resolver: resolver})
			if err != nil || strings.HasPrefix(dir, workspace) {
				t.Fatalf("a shared host ran the job's %s to pick its cache: %q, %v", name, dir, err)
			}
		})
	}
}

func TestNpmCacheProbeLeavesTheCacheDirectoryEmpty(t *testing.T) {
	input, state, _, _ := executorFixture(t)
	input.Entry.Directory = t.TempDir()
	home, tools := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$npm_config_logs_max\" = 0 ] || { mkdir -p \"$HOME/.npm/_logs\"; : > \"$HOME/.npm/_logs/debug-0.log\"; }\n" +
		"[ \"$npm_config_update_notifier\" = false ] || { mkdir -p \"$HOME/.npm\"; : > \"$HOME/.npm/_update-notifier-last-checked\"; }\n" +
		"echo 'npm error The cache option is protected, and can not be retrieved in this way' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(tools, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	environments := SDKEnvironment{Store: state, Base: procenv.Base{Ambient: []string{"HOME=" + home, "PATH=" + tools + ":/usr/bin:/bin"}, Masker: secrets.NewMasker()}}
	dir, err := environments.cacheDirectory(t.Context(), input, nodewire.CacheSpec{Resolver: "npm"})
	if err != nil || dir != filepath.Join(home, ".npm") {
		t.Fatalf("resolved npm cache=%q, %v", dir, err)
	}
	empty, err := (directoryCache{dir: dir}).empty()
	if err != nil || !empty {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("resolving the npm cache wrote into it (%v), so a fresh host never restores or saves it: %v", entries, err)
	}
}
