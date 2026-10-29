//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type ownerSourceEngine struct {
	claimEngine
	resolvedPins int
	ensured      int
}

func (r *ownerSourceEngine) Resolve(_ context.Context, pin engineid.Pin, target releaseasset.Target) (engine.Resolved, error) {
	r.resolvedPins++
	if pin != r.resolved.Identity.Pin || target != r.resolved.Target {
		return engine.Resolved{}, executorError(apierr.SpecDrift, "planning source changed its engine pin or target")
	}
	return r.resolved, nil
}

func (r *ownerSourceEngine) Ensure(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (engine.Resolved, error) {
	r.ensured++
	return r.claimEngine.Ensure(ctx, identity, target)
}

func TestOwnerHostAcquiresSealedSourceWithExplicitEnvironment(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "resumed"}[resumed], func(t *testing.T) {
			f, host, input := newOwnerFixture(t, resumed)
			root, sha := hostSourceCheckout(t)
			request := &f.snapshot.Run.Request.Request
			request.Source.Location = runspec.SourceLocation{LocalCheckout: root, PipelineDir: "."}
			request.Git.SHA, request.Git.Repository.Key = sha, "example.com/acme/repo"
			request.Git.DefaultBranch.Set, request.Git.DefaultBranch.Value = true, "main"
			request.Source.Entrypoint = "."
			captured := []string{"PATH=" + os.Getenv("PATH")}
			snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			request.Env, err = snapshots.Capture(t.Context(), captured)
			if err != nil {
				t.Fatal(err)
			}
			host.Snapshots = snapshots
			input.SealedRequest, _ = json.Marshal(request)
			input.Accepted = nil
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			resolved := engine.Resolved{Identity: engineid.Identity{Version: "v1.0.0", ManifestDigest: "verified", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.0.0"}}, Executable: exe, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
			if resumed {
				f.snapshot.Run.Spec.Request = f.snapshot.Run.Request
				f.snapshot.Run.Spec.Resolved.Engine = resolved.Identity
			}
			engines := &ownerSourceEngine{claimEngine: claimEngine{resolved: resolved}}
			installed := 0
			host.Acquire = nil
			host.Source = source.Options{
				Engines: engines, Target: resolved.Target, SDK: engineid.GoSDK,
				Base: procenv.Base{Local: true, Captured: []string{"PATH=/daemon/bin"}}, BuildEnvironment: sourcetest.BuildEnvironment,
				ReadPin: func(string, engineid.SDKKind) (engineid.Pin, error) { return resolved.Identity.Pin, nil },
				InstallSDK: func(_ context.Context, got engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
					directory, env := filepath.Join(request.Directory, request.PipelineDir), request.Environment
					installed++
					if !reflect.DeepEqual(got, resolved) || directory != root || !reflect.DeepEqual(env, captured) {
						t.Error("source installation lost its selected engine, checkout or explicit base environment")
					}
					return engine.SDKCommand{Directory: directory, Command: []string{"SDK-entry"}}, nil
				},
			}
			prepare := host.Prepare
			host.Prepare = func(ctx context.Context, in RunInput, entry *source.Entry) (OwnerPlanning, error) {
				if resumed {
					t.Fatal("resumed owner replanned")
				}
				if entry.SHA != sha || entry.RepoKey != string(request.Git.Repository.Key) || entry.DefaultBranch != "main" {
					t.Fatal("planning lost sealed checkout facts")
				}
				planning, err := prepare(ctx, in, entry)
				planning.Resolved.Engine = entry.Engine.Identity
				return planning, err
			}
			completion, err := host.Run(t.Context(), input)
			if err != nil || completion.Outcome != "success" {
				t.Fatalf("result=%+v error=%v", completion, err)
			}
			if installed != 1 || engines.ensured != map[bool]int{false: 0, true: 1}[resumed] || engines.resolvedPins != map[bool]int{false: 1, true: 0}[resumed] {
				t.Fatalf("installs=%d pin resolutions=%d sealed resolutions=%d", installed, engines.resolvedPins, engines.ensured)
			}
			if _, err := os.Stat(root); err != nil {
				t.Fatal("owner removed the user's checkout:", err)
			}
		})
	}
}

func TestOwnerHostSourceProjectionFailureFinishesOnlyUnacceptedRuns(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "resumed"}[resumed], func(t *testing.T) {
			f, host, input := newOwnerFixture(t, resumed)
			host.Acquire = nil
			_, err := host.Run(t.Context(), input)
			if err == nil || (f.finished.Transition.RunID != "") == resumed {
				t.Fatalf("finish=%+v error=%v", f.finished, err)
			}
		})
	}
}
