//go:build unix

package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestBurstUploadedSDKBuildsAndExecutesEveryAward(t *testing.T) {
	modules, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(modules)))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOPROXY", "off")
	repo, sha, selected := burstSDKSourceFixture(t)
	_, plan, err := sdkserve.DefaultOperations.Plan(t.Context(), burstSDKPipelineName, map[string]string{}, nodeproto.RunContext{Pipeline: burstSDKPipelineName})
	if err != nil {
		t.Fatal(err)
	}
	for _, placement := range []string{"here", "personal", "cloud"} {
		t.Run(placement, func(t *testing.T) {
			proof := controller.BurstExecutionProof{
				Pipeline: burstSDKPipelineName, Plan: plan, Engine: selected.Identity,
				Source: func(*testing.T) (string, string) { return repo, sha },
				Execute: func(options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
					executeBurstUploadedSDK(t, selected, options, award, owner)
				},
			}
			if placement == "cloud" {
				configureBurstCloudJob(t, &proof)
			}
			controller.BurstExecutionAcceptance(t, placement, proof)
		})
	}
}

func burstSDKSourceFixture(t *testing.T) (string, string, sourceBuildEngine) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	repo, _ := sourcetest.Repository(t)
	directory := filepath.Join(repo, ".sparkwing")
	for _, name := range []string{"go.mod", "go.sum"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			body = []byte(strings.Replace(string(body), "module github.com/sparkwing-dev/sparkwing", "module burstfixture", 1) + fmt.Sprintf("\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\nreplace github.com/sparkwing-dev/sparkwing => %q\n", root))
		}
		sourcetest.Write(t, filepath.Join(directory, name), string(body))
	}
	body, err := os.ReadFile(filepath.Join(root, "pkg/controller/testdata/burst-sdk/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	sourcetest.Write(t, filepath.Join(directory, "pipeline.go"), string(body))
	sourcetest.Write(t, filepath.Join(repo, "source-marker.txt"), "uploaded edited source\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "execution snapshot")
	sha := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	pin, err := engine.ReadPin(directory, engine.GoSDK)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selected := sourceBuildEngine{engine.Resolved{
		Identity: engine.Identity{Version: "development", ManifestDigest: "verified", Pin: pin},
		Target:   releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Executable: executable,
	}}
	return repo, sha, selected
}

func executeBurstUploadedSDK(t *testing.T, selected sourceBuildEngine, options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
	t.Helper()
	wire, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	private, err := client.NewNodeHostClient(options, award.RunID, award.JobID)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	state, err := nodehost.NewRemoteHostStore(wire, private, award.UnitID, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.ReadSnapshot(t.Context(), award.RunID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := source.ForExecution(*snapshot.Run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	access, err := client.NewSourceAccess(options, award.RunID, spec)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := source.Acquire(t.Context(), spec, source.Options{
		Workspace: t.TempDir(), Access: access, BundleTransport: options.HTTP.Transport,
		Engines: selected, Target: selected.Target, SDK: engine.GoSDK,
		Base: procenv.Base{Local: true, Captured: os.Environ()},
		InstallSDK: func(ctx context.Context, _ engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			request.ExecutableDirectory = t.TempDir()
			input, err := json.Marshal(request)
			if err != nil {
				return engine.SDKCommand{}, err
			}
			var output, diagnostics bytes.Buffer
			if err := sdkinstall.Run(ctx, "development", bytes.NewReader(input), &output, &diagnostics); err != nil {
				return engine.SDKCommand{}, fmt.Errorf("install SDK: %w: %s", err, &diagnostics)
			}
			var command engine.SDKCommand
			err = json.Unmarshal(output.Bytes(), &command)
			return command, err
		},
	})
	if err != nil {
		t.Fatalf("acquire uploaded SDK: %v", err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	executeBurstSDKProcess(t, options, award, owner, &burstSDKNativeExecution{
		entry: entry, serviceURL: func(handler http.Handler) string {
			url, paths := burstSDKPipeService(t, handler)
			for i, name := range []string{".burst-requests", ".burst-responses"} {
				if err := os.Symlink(paths[i], filepath.Join(entry.CheckoutDirectory(), name)); err != nil {
					t.Fatal(err)
				}
			}
			return url
		},
	})
}

type sourceBuildEngine struct{ engine.Resolved }

func (e sourceBuildEngine) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return e.Resolved, nil
}

func (e sourceBuildEngine) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return e.Resolved, nil
}
