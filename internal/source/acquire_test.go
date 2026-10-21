package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type engineResolver struct {
	resolved engine.Resolved
	err      error
	calls    []string
}

func TestAcquireDetectsUnacceptedTypeScriptAfterCheckout(t *testing.T) {
	directory, sha := sourcetest.Repository(t)
	pipeline := filepath.Join(directory, ".sparkwing")
	sourcetest.Write(t, filepath.Join(pipeline, "package.json"), `{"dependencies":{"@sparkwing/sdk":"1.2.3"}}`)
	options, _, _ := acquisitionOptions(t, pipeline)
	options.SDK = ""
	reached := errors.New("TypeScript pin selected")
	options.ReadPin = func(_ string, sdk engine.SDKKind) (engine.Pin, error) {
		if sdk != engine.TypeScriptSDK {
			return engine.Pin{}, errors.New("wrong SDK selected before checkout")
		}
		return engine.Pin{}, reached
	}
	_, err := source.Acquire(t.Context(), source.Spec{RepoKey: "local:" + directory, LocalCheckout: directory, SHA: sha}, options)
	if !errors.Is(err, reached) {
		t.Fatal(err)
	}
}

func (r *engineResolver) Resolve(_ context.Context, pin engine.Pin, target releaseasset.Target) (engine.Resolved, error) {
	r.calls = append(r.calls, "resolve")
	if r.resolved.Identity.Pin != pin || r.resolved.Target != target {
		return engine.Resolved{}, errors.New("wrong pin or target")
	}
	return r.resolved, r.err
}

func (r *engineResolver) Ensure(_ context.Context, identity engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
	r.calls = append(r.calls, "ensure")
	if r.resolved.Identity != identity || r.resolved.Target != target {
		return engine.Resolved{}, errors.New("wrong sealed identity or target")
	}
	return r.resolved, r.err
}

type cancelledEngine struct {
	source.EngineResolver
	cancel context.CancelCauseFunc
}

func (r cancelledEngine) Ensure(ctx context.Context, identity engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
	resolved, err := r.EngineResolver.Ensure(ctx, identity, target)
	r.cancel(lease.ErrLeaseLost)
	return resolved, err
}

func TestAcquireAuthorityLossBeforeInstallationStartsNoInstaller(t *testing.T) {
	for _, stage := range []string{"pin", "engine"} {
		t.Run(stage, func(t *testing.T) {
			dir, sha := sourcetest.Repository(t)
			options, resolver, installed := acquisitionOptions(t, filepath.Join(dir, ".sparkwing"))
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			if stage == "pin" {
				read := options.ReadPin
				options.ReadPin = func(path string, sdk engine.SDKKind) (engine.Pin, error) {
					pin, err := read(path, sdk)
					cancel(lease.ErrLeaseLost)
					return pin, err
				}
			} else {
				options.Engines = cancelledEngine{EngineResolver: resolver, cancel: cancel}
			}
			entry, err := source.Acquire(ctx, source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha, Engine: &resolver.resolved.Identity}, options)
			if entry != nil || !errors.Is(err, lease.ErrLeaseLost) || *installed != 0 {
				t.Fatalf("acquisition after loss at %s: entry=%v, error=%v, installs=%d", stage, entry, err, *installed)
			}
			if stage == "pin" && len(resolver.calls) != 0 {
				t.Fatal("lost source authority resolved an engine")
			}
		})
	}
}

func acquisitionOptions(t *testing.T, directory string) (source.Options, *engineResolver, *int) {
	t.Helper()
	pin := engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3", Manifest: "go.mod", InputsDigest: "sealed-pin"}
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"}
	resolver := &engineResolver{resolved: engine.Resolved{
		Identity: engine.Identity{Pin: pin, Version: "v1.2.3", ManifestDigest: "signed-manifest"},
		Target:   target, Executable: filepath.Join(t.TempDir(), "matched-engine"),
	}}
	installed := new(int)
	options := source.Options{
		Engines: resolver, Target: target, SDK: engine.GoSDK,
		Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment,
		ReadPin: func(checkout string, sdk engine.SDKKind) (engine.Pin, error) {
			if checkout != directory || sdk != pin.SDK {
				t.Fatalf("read pin from %s for %s", checkout, sdk)
			}
			return pin, nil
		},
		InstallSDK: func(_ context.Context, resolved engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			checkout := filepath.Join(request.Directory, request.PipelineDir)
			environment := request.Environment
			*installed++
			if len(resolver.calls) != 1 || !reflect.DeepEqual(resolved, resolver.resolved) || checkout != directory || len(environment) != 2 || request.SDK != engine.GoSDK {
				t.Fatalf("install did not receive the resolved engine and isolated environment: %+v, %s, %v", resolved, checkout, environment)
			}
			return engine.SDKCommand{Directory: checkout, Command: []string{filepath.Join(directory, "entry")}}, nil
		},
	}
	return options, resolver, installed
}

func TestAcquireResolvesEngineBeforeInstallation(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "planning", true: "execution"}[sealed], func(t *testing.T) {
			dir, sha := sourcetest.Repository(t)
			options, resolver, installed := acquisitionOptions(t, filepath.Join(dir, ".sparkwing"))
			spec := source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha}
			wantCall := "resolve"
			if sealed {
				spec.Engine = &resolver.resolved.Identity
				wantCall = "ensure"
			}
			entry, err := source.Acquire(t.Context(), spec, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := entry.Close(); err != nil {
					t.Error(err)
				}
			})
			if *installed != 1 || !reflect.DeepEqual(resolver.calls, []string{wantCall}) || entry.SHA != sha || !reflect.DeepEqual(entry.Engine, resolver.resolved) || len(entry.Command) != 1 {
				t.Fatalf("acquisition = %+v, engine calls %v, installs %d", entry, resolver.calls, *installed)
			}
		})
	}
}

func TestAcquireDoesNotInstallWithChangedPinOrRejectedEngine(t *testing.T) {
	for _, failure := range []string{"changed pin", "pin read", "engine policy"} {
		t.Run(failure, func(t *testing.T) {
			dir, _ := sourcetest.Repository(t)
			options, resolver, installed := acquisitionOptions(t, filepath.Join(dir, ".sparkwing"))
			spec := source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}
			sentinel := errors.New(failure)
			switch failure {
			case "changed pin":
				sealed := resolver.resolved.Identity
				sealed.Pin.InputsDigest = "previous-lockfile"
				spec.Engine = &sealed
			case "pin read":
				options.ReadPin = func(string, engine.SDKKind) (engine.Pin, error) { return engine.Pin{}, sentinel }
			case "engine policy":
				resolver.err = sentinel
			}
			entry, err := source.Acquire(t.Context(), spec, options)
			if err == nil || entry != nil || *installed != 0 {
				t.Fatalf("acquisition after %s = %+v, %v, installs %d", failure, entry, err, *installed)
			}
			if failure != "changed pin" && !errors.Is(err, sentinel) {
				t.Fatalf("lost failure: %v", err)
			}
		})
	}
}

func TestAcquireRemovesRemoteCheckoutAfterInstallationFailure(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: dir})
	for _, failure := range []string{"installer", "empty command", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			options, resolver, _ := acquisitionOptions(t, "")
			options.Workspace, options.Access = t.TempDir(), &access{}
			options.ReadPin = func(string, engine.SDKKind) (engine.Pin, error) { return resolver.resolved.Identity.Pin, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sentinel := errors.New("installer failed")
			options.InstallSDK = func(_ context.Context, _ engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
				switch failure {
				case "installer":
					return engine.SDKCommand{}, sentinel
				case "cancelled":
					cancel()
					return engine.SDKCommand{Directory: filepath.Join(request.Directory, request.PipelineDir), Command: []string{"entry"}}, nil
				default:
					return engine.SDKCommand{}, nil
				}
			}
			entry, err := source.Acquire(ctx, source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "trunk"}, options)
			if entry != nil || err == nil {
				t.Fatalf("failed installation = %+v, %v", entry, err)
			}
			if failure == "installer" && !errors.Is(err, sentinel) || failure == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost installation failure: %v", err)
			}
			remaining, err := os.ReadDir(options.Workspace)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("failed installation left workspace contents: %v, %v", remaining, err)
			}
		})
	}
}

func TestAcquireUsesSharedEnvironmentPolicyForGitAndInstallation(t *testing.T) {
	for _, failure := range []string{"none", "git", "install"} {
		t.Run(failure, func(t *testing.T) {
			dir, sha := sourcetest.Repository(t)
			options, resolver, _ := acquisitionOptions(t, filepath.Join(dir, ".sparkwing"))
			sentinel := errors.New("environment rejected")
			var kinds []procenv.Kind
			options.BuildEnvironment = func(kind procenv.Kind, base procenv.Base, rt procenv.Runtime) (procenv.Env, error) {
				kinds = append(kinds, kind)
				if !reflect.DeepEqual(base, options.Base) || !reflect.DeepEqual(rt, procenv.Runtime{}) {
					t.Fatal("source supplied ambient or runtime input to shared builder")
				}
				if kind == procenv.Git && failure == "git" || kind == procenv.Install && failure == "install" {
					return procenv.Env{}, sentinel
				}
				if kind == procenv.Git {
					return sourcetest.BuildEnvironment(kind, base, rt)
				}
				return procenv.Build(kind, procenv.Base{Local: true, Captured: []string{"PATH=/filtered/install"}}, rt)
			}
			installed := false
			options.InstallSDK = func(_ context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
				installed = true
				if !reflect.DeepEqual(selected, resolver.resolved) || request.Directory != dir || request.PipelineDir != ".sparkwing" || request.SDK != engine.GoSDK || !reflect.DeepEqual(request.Environment, []string{"PATH=/filtered/install"}) {
					t.Fatalf("selected-engine request=%+v", request)
				}
				return engine.SDKCommand{Directory: filepath.Join(dir, ".sparkwing"), Command: []string{"entry"}}, nil
			}
			entry, err := source.Acquire(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha}, options)
			if failure == "none" {
				if err != nil || entry == nil || !installed || !reflect.DeepEqual(kinds, []procenv.Kind{procenv.Git, procenv.Install}) {
					t.Fatalf("entry=%v kinds=%v install=%t err=%v", entry, kinds, installed, err)
				}
				if err := entry.Close(); err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, sentinel) || entry != nil || installed {
				t.Fatalf("environment failure continued: entry=%v installed=%t err=%v", entry, installed, err)
			}
		})
	}
}

func TestAcquirePassesSourceMaskRegistryToSelectedInstaller(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	options, _, _ := acquisitionOptions(t, filepath.Join(dir, ".sparkwing"))
	options.Base.Masker = secrets.NewMasker()
	const secret = "operator-registry-note-0123456789"
	options.Base.Masker.Register(secret)
	install := options.InstallSDK
	options.InstallSDK = func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
		if request.Masker == nil || request.Masker.Mask(secret) == secret {
			t.Fatal("source masks did not reach the selected installer")
		}
		return install(ctx, selected, request)
	}
	entry, err := source.Acquire(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha}, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
}
