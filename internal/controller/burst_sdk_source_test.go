//go:build unix

package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestBurstUploadedSDKBuildsAndExecutesEveryAward(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 10.1s measured in the short class; the full suite runs it")
	}
	modules, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(modules)))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOPROXY", "off")
	t.Setenv("TOKEN", "ambient-requester-value-never-forward")
	repo, sha, engines, selected := burstSDKSourceFixture(t, "")
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

// burstSDKSourceFixture commits the burst SDK pipeline, followed by appended source when it is not empty.
func burstSDKSourceFixture(t *testing.T, appended string) (string, string, *engine.Resolver, engine.Resolved) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := burstSDKModule(t, filepath.Clean(filepath.Join(filepath.Dir(file), "../..")))
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
	body, err := os.ReadFile(filepath.Join(root, "internal/controller/testdata/burst-sdk/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	sourcetest.Write(t, filepath.Join(directory, "pipeline.go"), string(body)+appended)
	sourcetest.Write(t, filepath.Join(repo, "source-marker.txt"), "uploaded edited source\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "execution snapshot")
	sha := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	pin, err := engine.ReadPin(directory, engineid.GoSDK)
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

func burstSDKModule(t *testing.T, root string) string {
	t.Helper()
	destination := t.TempDir()
	files := sourcetest.Git(t, root, "ls-files", "-z")
	for _, name := range strings.Split(strings.TrimSuffix(files, "\x00"), "\x00") {
		from, to := filepath.Join(root, name), filepath.Join(destination, name)
		info, err := os.Lstat(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(from)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, to); err != nil {
				t.Fatal(err)
			}
			continue
		}
		body, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, body, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
	return destination
}

func executeBurstUploadedSDK(t *testing.T, engines *engine.Resolver, selected engine.Resolved, options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
	t.Helper()
	var renewed atomic.Bool
	transport := options.HTTP.Transport
	connection := *options.HTTP
	connection.Transport = burstSourceRoundTrip(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err == nil && response.StatusCode == http.StatusOK && strings.HasSuffix(request.URL.Path, "/heartbeat") {
			renewed.Store(true)
		}
		return response, err
	})
	options.HTTP = &connection
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
	native := &burstSDKNativeExecution{
		engines: engines,
		source: &source.Options{
			Workspace: t.TempDir(), Access: access, BundleTransport: options.HTTP.Transport,
			Engines: engines, Target: selected.Target, SDK: engineid.GoSDK,
			Base: procenv.Base{Ambient: os.Environ(), SubmitEnvAllow: []string{"GOCACHE", "GOMODCACHE"}},
		},
		acquire: func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
			if !renewed.Load() {
				t.Error("uploaded source installation preceded the awarded job's first renewal")
			}
			return source.Acquire(ctx, spec, options)
		},
	}
	native.serviceURL = func(handler http.Handler) string {
		url, paths := burstSDKPipeService(t, handler)
		for i, name := range []string{".burst-requests", ".burst-responses"} {
			if err := os.Symlink(paths[i], filepath.Join(native.entry.CheckoutDirectory(), name)); err != nil {
				t.Fatal(err)
			}
		}
		return url
	}
	executeBurstSDKProcess(t, options, award, owner, native)
	if native.entry == nil {
		t.Fatal("claimed host did not acquire source")
	}
	if _, err := os.Stat(native.entry.Command[0]); !os.IsNotExist(err) {
		t.Fatalf("closed source retained SDK executable: %v", err)
	}
}

type burstSourceRoundTrip func(*http.Request) (*http.Response, error)

func (f burstSourceRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
