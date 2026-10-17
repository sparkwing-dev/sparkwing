//go:build unix

package controller_test

import (
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
	repo, sha, engines, selected := burstSDKSourceFixture(t)
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
					executeBurstUploadedSDK(t, engines, selected, options, award, owner)
				},
			}
			if placement == "cloud" {
				configureBurstCloudJob(t, &proof)
			}
			controller.BurstExecutionAcceptance(t, placement, proof)
		})
	}
}

func burstSDKSourceFixture(t *testing.T) (string, string, *engine.Resolver, engine.Resolved) {
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
	engines, err := engine.NewResolver(engine.Config{BuildVersion: "development", CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := engines.Resolve(t.Context(), pin, releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	return repo, sha, engines, selected
}

func executeBurstUploadedSDK(t *testing.T, engines *engine.Resolver, selected engine.Resolved, options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
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
		Engines: engines, Target: selected.Target, SDK: engine.GoSDK,
		Base: procenv.Base{Local: true, Captured: os.Environ()},
	})
	if err != nil {
		t.Fatalf("acquire uploaded SDK: %v", err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(entry.Command[0]); !os.IsNotExist(err) {
			t.Errorf("closed source retained SDK executable: %v", err)
		}
	}()
	executeBurstSDKProcess(t, options, award, owner, &burstSDKNativeExecution{
		entry: entry, engines: engines, serviceURL: func(handler http.Handler) string {
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
