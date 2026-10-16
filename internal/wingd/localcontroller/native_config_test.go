package localcontroller

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type nativeState struct {
	nodehost.NativeHostStore
	snapshot store.RunSnapshot
	node     store.NodeRow
}

func (s *nativeState) ReadNode(ctx context.Context, _ store.NodeRef) (store.NodeRow, error) {
	return s.node, context.Cause(ctx)
}

func (s *nativeState) ReadSnapshot(ctx context.Context, _ string) (store.RunSnapshot, error) {
	return s.snapshot, context.Cause(ctx)
}

type nativeEngine struct {
	resolve func(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error)
}

func (e nativeEngine) Resolve(ctx context.Context, pin engine.Pin, target releaseasset.Target) (engine.Resolved, error) {
	if e.resolve != nil {
		return e.resolve(ctx, pin, target)
	}
	return engine.Resolved{}, errors.New("source boundary")
}

func (nativeEngine) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("engine boundary")
}

func nativeConfigFixture(t testing.TB) (NativeConfig, *nativeState) {
	t.Helper()
	_, authority, _, _ := localClaimsFixture(t)
	principal := authority.snapshot.Authorities[0].Principal
	home := t.TempDir()
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	seal, err := snapshots.Capture(t.Context(), []string{"RETAINED=registration"})
	if err != nil {
		t.Fatal(err)
	}
	state := &nativeState{snapshot: store.RunSnapshot{Run: store.RunRow{
		ID: "run", Team: store.DefaultTeam,
		Request: runspec.SealedRequest{Request: runspec.Request{Team: string(store.DefaultTeam), Env: seal}},
	}}}
	return NativeConfig{
		Local: func(store.NodeRow) bool { return true }, Home: home, Version: "v0.71.0", Store: state, Parent: authority, Bindings: authority,
		Principal: principal, Refresh: func(context.Context) (capability.Principal, error) { return principal, nil },
		Renew:    func(context.Context, string, bool) (store.LeaseBeat, error) { return store.LeaseBeat{}, nil },
		Source:   source.Options{Engines: nativeEngine{}, Base: procenv.Base{Local: true}},
		Services: completeServiceFixture,
		Storage: func(context.Context, capability.Principal) (nodehost.StorageBinding, error) {
			return nodehost.StorageBinding{}, nil
		},
		Logs: func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error) {
			return nil, errors.New("unused")
		},
	}, state
}

func TestNativeServicesUsesSharedInterfacesAndCapturedEnvironment(t *testing.T) {
	cfg, state := nativeConfigFixture(t)
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	if services.Config.Store.(logHostStore).NativeHostStore != state || !services.Config.Source.Base.Local || !reflect.DeepEqual(services.Config.Source.Base.Captured, []string{"RETAINED=registration"}) {
		t.Fatal("native composition replaced state or captured inputs")
	}
	owner, claimed, evaluator, err := nodehost.NewNativeHost(services.Config)
	if err != nil || owner == nil || claimed == nil || evaluator == nil {
		t.Fatalf("shared native hosts cannot use the supplied interfaces: %v", err)
	}
}

func TestNativeOwnerResolvesUnacceptedLocalTypeScriptPin(t *testing.T) {
	cfg, state := nativeConfigFixture(t)
	root, _ := sourcetest.Repository(t)
	sourcetest.Write(t, filepath.Join(root, ".sparkwing", "package.json"), `{"dependencies":{"@sparkwing/sdk":"1.2.3"}}`)
	sourcetest.Git(t, root, "add", ".")
	sourcetest.Git(t, root, "commit", "-qm", "typescript pipeline")
	request := &state.snapshot.Run.Request.Request
	request.Git = runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey("local:" + root)}, SHA: sourcetest.Git(t, root, "rev-parse", "HEAD")}
	request.Source.Location = runspec.SourceLocation{LocalCheckout: root, PipelineDir: ".sparkwing"}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(cfg.Home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	request.Env, err = snapshots.Capture(t.Context(), sourcetest.Environment(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Principal.Capability.Unit = capability.UnitTrigger
	cfg.Principal.Capability.Binding.Kind = capability.BindingTrigger
	cfg.Principal.Capability.Binding.NodeID = ""
	cfg.Refresh = func(context.Context) (capability.Principal, error) { return cfg.Principal, nil }
	cfg.Storage = func(context.Context, capability.Principal) (nodehost.StorageBinding, error) {
		data := &nativeStorageFixture{}
		return nodehost.StorageBinding{Bytes: data, Records: data, Authority: blob.Authority{CapabilityRef: cfg.Principal.Capability.Prefix}}, nil
	}
	boundary := errors.New("verified engine resolution boundary")
	resolved := false
	cfg.Source.SDK = engine.GoSDK
	cfg.Source.Target = releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"}
	cfg.Source.BuildEnvironment = sourcetest.BuildEnvironment
	cfg.Source.Engines = nativeEngine{resolve: func(_ context.Context, pin engine.Pin, _ releaseasset.Target) (engine.Resolved, error) {
		resolved = true
		if pin.SDK != engine.TypeScriptSDK || pin.Version != "1.2.3" || pin.Manifest != "package.json" || pin.InputsDigest == "" {
			t.Fatalf("owner selected a different SDK pin: %+v", pin)
		}
		return engine.Resolved{}, boundary
	}}
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	spec, err := source.ForRequest(state.snapshot.Run.Request)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := source.Acquire(t.Context(), spec, services.Config.Source)
	if entry != nil || !resolved || !errors.Is(err, boundary) {
		t.Fatalf("local TypeScript owner did not reach its engine: resolved=%t entry=%v error=%v", resolved, entry, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sparkwing", "package.json")); err != nil {
		t.Fatal("source cleanup removed the local checkout")
	}
}

func TestNativeSDKSelectionPreservesAcceptedEngineAndRefusesInvalidSource(t *testing.T) {
	for _, test := range []struct {
		name, manifest, pipeline string
		accepted                 bool
		want                     engine.SDKKind
		refused                  bool
	}{
		{name: "go", pipeline: ".sparkwing", want: engine.GoSDK},
		{name: "typescript", manifest: `{"devDependencies":{"@sparkwing/sdk":"1.2.3"}}`, pipeline: "pipelines", want: engine.TypeScriptSDK},
		{name: "malformed", manifest: `{"dependencies":`, pipeline: ".sparkwing", refused: true},
		{name: "escape", pipeline: "../outside", refused: true},
		{name: "accepted", manifest: `{"dependencies":`, pipeline: ".sparkwing", accepted: true, want: engine.TypeScriptSDK},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, state := nativeConfigFixture(t)
			root := t.TempDir()
			pipeline := test.pipeline
			if test.refused {
				pipeline = ".sparkwing"
			}
			if err := os.Mkdir(filepath.Join(root, pipeline), 0o700); err != nil {
				t.Fatal(err)
			}
			if test.manifest != "" {
				sourcetest.Write(t, filepath.Join(root, pipeline, "package.json"), test.manifest)
			}
			state.snapshot.Run.Request.Request.Git.Repository.Key = runspec.RepoKey("local:" + root)
			state.snapshot.Run.Request.Request.Source.Location = runspec.SourceLocation{LocalCheckout: root, PipelineDir: test.pipeline}
			if test.accepted {
				state.snapshot.Run.Spec = &runspec.Spec{Resolved: runspec.Resolved{Engine: engine.Identity{Pin: engine.Pin{SDK: test.want}}}}
			}
			opened := false
			services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
				opened = true
				return &processListener{closed: make(chan struct{})}, nil
			})
			if services.Close != nil {
				t.Cleanup(func() {
					if err := services.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			if test.refused {
				if err == nil || opened || services.Close != nil {
					t.Fatalf("invalid local source reached process transport: opened=%t error=%v", opened, err)
				}
			} else if err != nil || !opened || services.Config.Source.SDK != test.want {
				t.Fatalf("source SDK=%q, opened=%t error=%v", services.Config.Source.SDK, opened, err)
			}
		})
	}
}

type nativeStorageFixture struct {
	blob.Store
	blob.RecordStore
}

func TestNativeOwnerEvaluationUsesItsOwnBinderBeforeAnyWorkAward(t *testing.T) {
	cfg, _ := nativeConfigFixture(t)
	cfg.Principal.Capability.Unit = capability.UnitTrigger
	cfg.Principal.Capability.Binding.Kind = capability.BindingTrigger
	cfg.Principal.Capability.Binding.NodeID = ""
	cfg.Refresh = func(context.Context) (capability.Principal, error) { return cfg.Principal, nil }
	cfg.Storage = func(context.Context, capability.Principal) (nodehost.StorageBinding, error) {
		data := &nativeStorageFixture{}
		return nodehost.StorageBinding{Bytes: data, Records: data, Authority: blob.Authority{CapabilityRef: cfg.Principal.Capability.Prefix}}, nil
	}
	calls := 0
	cfg.Evaluate = func(_ context.Context, input nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
		calls++
		return nodehost.ProcessGrant{Binding: nodeproto.ProcessBinding{RunID: input.Accepted.RunID, JobID: input.NodeID}, Revoke: func() error { return nil }}, nil
	}
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, generation := range []int64{0, 1, 19} {
		grant, err := services.Config.Evaluation.Bind(t.Context(), nodehost.EvaluationInput{Accepted: nodehost.AcceptedRun{RunID: "run"}, NodeID: "source", Generation: generation})
		if err != nil || grant.Binding.JobID != "source" {
			t.Fatalf("owner evaluation required a node award: %+v %v", grant, err)
		}
		if err := grant.Revoke(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatal("owner evaluation bypassed its binder")
	}
}

func FuzzNativeBindingsRejectForeignScopeBeforeTransport(f *testing.F) {
	f.Add(uint8(1), "foreign", false)
	f.Add(uint8(2), "foreign", true)
	f.Add(uint8(4), "foreign", false)
	f.Fuzz(func(t *testing.T, fields uint8, suffix string, refreshed bool) {
		cfg, state := nativeConfigFixture(t)
		fields = fields%7 + 1
		if refreshed {
			principal := cfg.Principal
			if fields&1 != 0 {
				principal.Authority.ID += "-foreign-" + suffix
			}
			if fields&2 != 0 {
				principal.Capability.Team += "-foreign-" + suffix
			}
			if fields&4 != 0 {
				principal.Capability.Binding.RunID += "-foreign-" + suffix
			}
			cfg.Refresh = func(context.Context) (capability.Principal, error) { return principal, nil }
		} else {
			if fields&1 != 0 {
				state.snapshot.Run.ID += "-foreign-" + suffix
			}
			if fields&2 != 0 {
				state.snapshot.Run.Team += store.Team("-foreign-" + suffix)
			}
			if fields&4 != 0 {
				state.snapshot.Run.Request.Request.Team += "-foreign-" + suffix
			}
		}
		opened := false
		services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
			opened = true
			return nil, errors.New("unexpected transport")
		})
		if err == nil || opened || services.Close != nil {
			t.Fatal("foreign state reached process service construction")
		}
	})
}

func TestNativeOwnerEvaluationHasProductionBinder(t *testing.T) {
	cfg, state := nativeConfigFixture(t)
	claims, authority, _, _ := localClaimsFixture(t)
	principal, _ := claims.current(t.Context())
	principal.Class = capability.CredentialBearer
	principal.Capability.Audience = capability.Controller
	principal.Capability.Unit, principal.Capability.UnitID = capability.UnitTrigger, "owner-unit"
	principal.Capability.Purpose, principal.Capability.Issuance = capability.PurposeTrigger, capability.Award
	principal.Capability.Grants = []capability.Grant{capability.DataRead, capability.NodeReport}
	principal.Capability.Digest = sha256.Sum256([]byte("owner-token"))
	authority.snapshot.Authorities[0].Principal = principal
	authority.snapshot.Binding.Current = principal.Capability.Binding
	cfg.Principal, cfg.Parent, cfg.Bindings = principal, authority, authority
	cfg.Refresh = func(context.Context) (capability.Principal, error) { return principal, nil }
	cfg.Storage = func(context.Context, capability.Principal) (nodehost.StorageBinding, error) {
		data := &nativeStorageFixture{}
		return nodehost.StorageBinding{Bytes: data, Records: data, Authority: blob.Authority{CapabilityRef: principal.Capability.Prefix}}, nil
	}
	state.node = store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Spec: []byte(`{"id":"job"}`), SpecHash: "source-hash"}
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	grant, err := services.Config.Evaluation.Bind(t.Context(), nodehost.EvaluationInput{Accepted: nodehost.AcceptedRun{RunID: "run"}, NodeID: "job", SpecHash: "source-hash"})
	if err != nil {
		t.Fatal(err)
	}
	if grant.Binding.JobID != "job" || grant.Binding.Routes.Bearer == "" || grant.Revoke == nil {
		t.Fatalf("incomplete owner process: %+v", grant)
	}
	if err := grant.Revoke(); err != nil {
		t.Fatal(err)
	}
}
