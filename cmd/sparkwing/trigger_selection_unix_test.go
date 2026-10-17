//go:build unix

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func selectedRevisionGit(t *testing.T) string {
	t.Helper()
	root, bin := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
while [ "$1" = -c ]; do shift 2; done
if [ "$1" = -C ]; then shift 2; fi
case "$*" in
  'rev-parse --show-toplevel') printf '%s\n' '` + root + `' ;;
  'rev-parse '*release*) printf '%s\n' bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb ;;
  'rev-parse '*) printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
  'remote get-url origin') printf '%s\n' https://git.example/acme/repo.git ;;
  'symbolic-ref '*) printf '%s\n' refs/remotes/origin/main ;;
  'for-each-ref '*) printf '%s\n' refs/remotes/origin/main ;;
  'ls-tree '*package.json*) exit 42 ;;
  'ls-tree '*) printf '%s\n' .sparkwing/sparkwing.yaml ;;
  'show '*bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:*)
    printf 'pipelines:\n  - name: deploy\n    entrypoint: ReleaseDeploy\n    profile: prod\n' ;;
  'show '*) printf 'pipelines:\n  - name: deploy\n    entrypoint: DevDeploy\n    profile: dev\n' ;;
  *) exit 43 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(root, "missing-config.yaml"))
	t.Chdir(root)
	return root
}

func TestTriggerCommandGuardsTheSelectedRevision(t *testing.T) {
	selectedRevisionGit(t)
	for _, profile := range []string{"prod", "dev"} {
		t.Run(profile, func(t *testing.T) {
			err := runTriggerCommandArgs(t.Context(), []string{"deploy", "--ref=release", "--profile=" + profile, "--detach"}, io.Discard, prepareNativeTrigger,
				func(_ context.Context, _ string) (client.EngineOptions, func(), error) {
					t.Fatal("source inspection crossed its fixture boundary")
					return client.EngineOptions{}, nil, nil
				})
			if err == nil {
				t.Fatal("source inspection unexpectedly succeeded")
			}
			if profile == "prod" {
				if !strings.Contains(err.Error(), "exit status 42") {
					t.Fatalf("selected profile did not reach source inspection: %v", err)
				}
			} else if !strings.Contains(err.Error(), "pinned to profile prod") {
				t.Fatalf("HEAD's profile bypassed the selected pin: %v", err)
			}
		})
	}
}

func TestTriggerSelectedConfigurationIgnoresNestedAndDirtyFiles(t *testing.T) {
	root := selectedRevisionGit(t)
	nested := filepath.Join(root, "nested", ".sparkwing")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "sparkwing.yaml"), []byte("pipelines: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(nested))
	_, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy", profile: "dev", ref: "release"})
	if err == nil || !strings.Contains(err.Error(), "pinned to profile prod") {
		t.Fatalf("nested dirty configuration hid the selected root pin: %v", err)
	}
}

func TestTriggerSelectedConfigurationRefusesMalformedProject(t *testing.T) {
	selectedRevisionGit(t)
	git := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "git")
	raw, err := os.ReadFile(git)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), `printf 'pipelines:\n  - name: deploy\n    entrypoint: ReleaseDeploy\n    profile: prod\n'`, `printf 'pipelines: [\n'`, 1)
	if script == string(raw) {
		t.Fatal("fixture did not replace the selected project")
	}
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy", profile: "prod", ref: "release"})
	if err == nil || !strings.Contains(err.Error(), "line") || strings.Contains(err.Error(), "exit status 42") {
		t.Fatalf("malformed selected project reached inspection: %v", err)
	}
}
