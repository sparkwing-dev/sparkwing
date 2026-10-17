//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func selectedRevisionRepo(t *testing.T) string {
	t.Helper()
	home, root := t.TempDir(), initSnapshotRepo(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing-config.yaml"))
	directory := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"prod", "dev"} {
		entrypoint := "ReleaseDeploy"
		if profile == "dev" {
			entrypoint = "DevDeploy"
		}
		writeSDKFixture(t, directory, `package main
import("context";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"deploy",func(context.Context)error{return nil});return nil
}
func main(){sw.Register("`+entrypoint+`",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
		writeSnapshotFile(t, root, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy\n    entrypoint: "+entrypoint+"\n    profile: "+profile+"\n", 0o600)
		runSnapshotGit(t, root, "add", ".")
		runSnapshotGit(t, root, "commit", "--quiet", "-m", profile)
		if profile == "prod" {
			runSnapshotGit(t, root, "tag", "release")
		}
	}
	runSnapshotGit(t, root, "remote", "add", "origin", "https://git.example/acme/repo.git")
	runSnapshotGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	t.Chdir(root)
	return root
}

func TestTriggerCommandGuardsTheSelectedRevision(t *testing.T) {
	selectedRevisionRepo(t)
	for _, profile := range []string{"prod", "dev"} {
		t.Run(profile, func(t *testing.T) {
			boundary := errors.New("controller connection boundary")
			connected := false
			err := runTriggerCommandArgs(t.Context(), []string{"deploy", "--ref=release", "--profile=" + profile, "--detach"}, io.Discard, prepareNativeTrigger,
				func(_ context.Context, selected string) (client.EngineOptions, func(), error) {
					connected = true
					if selected != "prod" {
						t.Fatalf("connected with profile %q", selected)
					}
					return client.EngineOptions{}, nil, boundary
				})
			if profile == "prod" {
				if !connected || !errors.Is(err, boundary) {
					t.Fatalf("selected source did not finish inspection: %v", err)
				}
			} else if connected || err == nil || !strings.Contains(err.Error(), "pinned to profile prod") {
				t.Fatalf("HEAD's profile bypassed the selected pin: %v", err)
			}
		})
	}
}

func TestTriggerSelectedConfigurationIgnoresNestedAndDirtyFiles(t *testing.T) {
	root := selectedRevisionRepo(t)
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
	root := selectedRevisionRepo(t)
	writeSnapshotFile(t, root, ".sparkwing/sparkwing.yaml", "pipelines: [\n", 0o600)
	runSnapshotGit(t, root, "commit", "--quiet", "-am", "malformed project")
	runSnapshotGit(t, root, "tag", "--force", "release")
	_, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy", profile: "prod", ref: "release"})
	if err == nil || !strings.Contains(err.Error(), "line") {
		t.Fatalf("malformed selected project reached inspection: %v", err)
	}
}
