package localcontroller

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NativeConfig binds one authenticated award to shared state and authority services.
type NativeConfig struct {
	SubmittedEnv   map[string][]byte
	SubmitAccept   procenv.SubmitAccept
	Local          func(store.NodeRow) bool
	Home           string
	Version        string
	Store          nodehost.NativeHostStore
	Parent         capability.AuthenticationReader
	Bindings       capability.BindingChecker
	Principal      capability.Principal
	Refresh        func(context.Context) (capability.Principal, error)
	Renew          func(context.Context, string, bool) (store.LeaseBeat, error)
	Source         source.Options
	Services       func(context.Context, capability.Principal, nodeproto.JobSpec) (nodehost.ProcessServices, error)
	Storage        func(context.Context, capability.Principal) (nodehost.StorageBinding, error)
	Logs           func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error)
	Evaluate       func(context.Context, nodehost.EvaluationInput) (nodehost.ProcessGrant, error)
	ExpansionActor func(context.Context, nodehost.RunInput, store.NodeRow) (store.Actor, error)
}

// NativeServices constructs the shared host services without opening a state store.
func NativeServices(ctx context.Context, cfg NativeConfig) (runners.NativeServices, error) {
	return nativeServices(ctx, cfg, net.Listen)
}

func nativeServices(ctx context.Context, cfg NativeConfig, listen func(string, string) (net.Listener, error)) (_ runners.NativeServices, err error) {
	if cfg.Store == nil || cfg.Parent == nil || cfg.Bindings == nil || cfg.Refresh == nil || cfg.Renew == nil || cfg.Principal.Capability.Team == "" || cfg.Principal.Capability.Binding.RunID == "" {
		return runners.NativeServices{}, errors.New("local native host requires authenticated state, authority, renewal and run bindings")
	}
	home, version, sourceOptions := cfg.Home, cfg.Version, cfg.Source
	current := func(ctx context.Context) (capability.Principal, error) {
		principal, err := cfg.Refresh(ctx)
		if err == nil && (principal.Authority != cfg.Principal.Authority || principal.Capability.Team != cfg.Principal.Capability.Team || principal.Capability.Binding.RunID != cfg.Principal.Capability.Binding.RunID) {
			err = errors.New("local host authority changed")
		}
		return principal, err
	}
	if _, err := current(ctx); err != nil {
		return runners.NativeServices{}, err
	}
	if cfg.Principal.Capability.Unit == capability.UnitTrigger && cfg.Local == nil {
		return runners.NativeServices{}, &apierr.Error{Code: apierr.PolicyMissing, Message: "owner hosting requires canonical placement eligibility"}
	}
	if cfg.Services == nil || cfg.Storage == nil || cfg.Logs == nil {
		return runners.NativeServices{}, errors.New("local native host requires process services, byte/record storage and shared log publication")
	}
	var storage nodehost.StorageBinding
	if cfg.Principal.Capability.Unit != capability.UnitNode {
		storage, err = cfg.Storage(ctx, cfg.Principal)
		if err != nil {
			return runners.NativeServices{}, err
		}
		if storage.Bytes == nil || storage.Records == nil || storage.Authority.CapabilityRef != cfg.Principal.Capability.Prefix {
			return runners.NativeServices{}, errors.New("local native host storage is incomplete or belongs to another award")
		}
	}
	admission, err := localMachineAdmission(home, version)
	if err != nil {
		return runners.NativeServices{}, err
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		return runners.NativeServices{}, err
	}
	run, err := cfg.Store.ReadSnapshot(ctx, cfg.Principal.Capability.Binding.RunID)
	if err != nil {
		return runners.NativeServices{}, err
	}
	if run.Run.ID != cfg.Principal.Capability.Binding.RunID || string(run.Run.Team) != cfg.Principal.Capability.Team || run.Run.Request.Request.Team != cfg.Principal.Capability.Team {
		return runners.NativeServices{}, nodeClaimMismatch()
	}
	var captured []string
	if sourceOptions.Base.Local {
		captured, err = snapshots.Load(ctx, run.Run.Request.Request.Env)
		if err != nil {
			return runners.NativeServices{}, err
		}
	}
	masker := sourceOptions.Base.Masker
	if masker == nil {
		masker = secrets.NewMasker()
	}
	sourceOptions.Base.Captured, sourceOptions.Base.Masker = captured, masker
	if !sourceOptions.Base.Local {
		sourceOptions.Base.Ambient = withoutLocalSecrets(sourceOptions.Base.Ambient, run.Run.Request.Request.LocalSecretNames, masker)
		if len(run.Run.Request.Request.Env.Names) > 0 || len(cfg.SubmittedEnv) > 0 {
			if err := runspec.VerifyEnvironment(run.Run.Request.Request.Env, cfg.SubmittedEnv); err != nil {
				return runners.NativeServices{}, err
			}
		}
		sourceOptions.Base, err = procenv.WithSubmitted(sourceOptions.Base, cfg.SubmittedEnv, cfg.SubmitAccept)
		if err != nil {
			return runners.NativeServices{}, err
		}
	}
	if run.Run.Spec != nil {
		sourceOptions.SDK = run.Run.Spec.Resolved.Engine.Pin.SDK
	} else {
		sourceOptions.SDK = ""
	}
	authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: cfg.Parent, Bindings: cfg.Bindings})
	if err != nil {
		return runners.NativeServices{}, err
	}
	closeResources := authorities.Close
	defer func() {
		if err != nil {
			err = errors.Join(err, closeResources())
		}
	}()
	claims := &nodeClaims{auth: capability.NewAuthenticator(cfg.Parent), bindings: cfg.Bindings, masker: masker, current: current, readNode: cfg.Store.ReadNode, services: cfg.Services}
	masks := nodehost.NewMaskRegistry(masker)
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	listener, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		return runners.NativeServices{}, err
	}
	serve, closeHTTP := localProcessHTTP(ctx, listener, router, authorities.Close)
	logs := &localLogs{current: current, claims: claims, readNode: cfg.Store.ReadNode, upload: cfg.Logs, masker: masker, run: run.Run.ID}
	closeResources = func() error { return errors.Join(logs.close(context.WithoutCancel(ctx)), closeHTTP()) }
	processes := &nodehost.NativeProcesses{Authorities: authorities, Masks: masks, URL: "http://" + listener.Addr().String()}
	claims.processes = processes
	processes.Log = logs.write
	delegate := func(ctx context.Context, node string, generation, attempt int64) (nodehost.ProcessGrant, error) {
		grant, err := claims.delegate(ctx, node, generation, attempt)
		if err == nil {
			serve()
		}
		return grant, err
	}

	outputs := nodehost.OutputRecords{Bind: func(_ context.Context, input nodehost.NodeInput) (nodehost.JobUpload, error) {
		authority := input.Storage.Authority
		if !authority.Upload.Set {
			return nodehost.JobUpload{}, errors.New("job output requires authenticated upload identity")
		}
		return nodehost.JobUpload{Identity: authority.Upload.Value, Authority: authority, Key: blob.ResultRecordKey(authority.Attempt.Value, authority.Repo, authority.Ref.Scope, run.Run.Pipeline, blob.KindOutput)}, nil
	}}
	artifacts := nodehost.ArtifactRecords{Bind: func(ctx context.Context, input nodehost.NodeInput) (nodehost.JobUpload, error) {
		upload, err := outputs.Bind(ctx, input)
		upload.Key.Kind = blob.KindManifest
		return upload, err
	}, Producer: func(ctx context.Context, input nodehost.NodeInput, producer string) (blob.RecordKey, error) {
		row, err := cfg.Store.ReadNode(ctx, store.NodeRef{RunID: input.Accepted.RunID, NodeID: producer})
		if err != nil {
			return blob.RecordKey{}, err
		}
		if row.Status != store.StatusDone || row.ManifestRecord == nil {
			return blob.RecordKey{}, blob.ErrNotFound
		}
		return *row.ManifestRecord, nil
	}}
	caches := nodehost.DepcacheRecords{Bind: func(ctx context.Context, input nodehost.NodeInput, key blob.DepcacheKey) (nodehost.JobUpload, error) {
		upload, err := outputs.Bind(ctx, input)
		upload.Key.Kind, upload.Key.Name, upload.Key.Scope.Key = blob.KindDepcache, key.Name, key.Key
		return upload, err
	}, Resolve: func(ctx context.Context, input nodehost.NodeInput, cache nodeproto.CacheSpec) (string, error) {
		return processes.Session.Environments.CacheDirectory(ctx, input, cache)
	}}
	follower := nodehost.StateFollower{Store: cfg.Store, Subscribe: localStateSubscription}
	config := nodehost.NativeHostConfig{
		Store: logHostStore{NativeHostStore: cfg.Store, logs: logs, unit: cfg.Principal.Capability.Unit}, Lease: lease.Keeper{Lease: limits.DefaultLease, Interval: limits.Renew(limits.DefaultLease)},
		Source: sourceOptions, Snapshots: snapshots, Masks: masks, Processes: processes, Router: router,
		Validation: nodeproto.ValidationContext{Placement: store.RequestPlacement(run.Run.Request.Request)},
		Admission:  admission,
		Owner:      nodehost.OwnerHost{Renew: localOwnerRenew(current, cfg.Renew), Local: cfg.Local, Wait: follower.Wait, WaitNode: follower.WaitNode, AdoptClaim: claims.adopt, ExpansionActor: cfg.ExpansionActor},
		Planner: nodehost.SDKPlanner{Log: func(ctx context.Context, frame nodeproto.FrameLog) error {
			return processes.Log(ctx, run.Run.ID, "", frame)
		}},
		Executor: nodehost.ExecutorDependencies{PublishOutput: outputs.PublishOutput, Annotate: claims.annotate, PrepareLayer: func(ctx context.Context, layer nodehost.LayerSpec, input nodehost.NodeInput, job nodeproto.JobSpec) (nodehost.ExecutorFinalizer, error) {
			if layer.Name == "caches" {
				return caches.Prepare(ctx, layer, input, job)
			}
			return artifacts.Prepare(ctx, layer, input, job)
		}, BindStorage: func(ctx context.Context, input nodehost.NodeInput) (nodehost.StorageBinding, error) {
			principal, err := claims.principal(ctx, input.NodeID, input.Generation, input.Attempt)
			if err != nil {
				return nodehost.StorageBinding{}, err
			}
			return cfg.Storage(ctx, principal)
		}, Bind: func(ctx context.Context, input nodehost.NodeInput) (nodehost.ProcessGrant, error) {
			return delegate(ctx, input.NodeID, input.Generation, input.Attempt)
		}},
		Evaluation: nodehost.EvaluationDependencies{
			Bind: func(ctx context.Context, input nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
				if input.Accepted.RunID != run.Run.ID || input.NodeID == "" || input.Generation < 0 {
					return nodehost.ProcessGrant{}, nodeClaimMismatch()
				}
				if _, err := current(ctx); err != nil {
					return nodehost.ProcessGrant{}, err
				}
				evaluate := cfg.Evaluate
				if evaluate == nil {
					evaluate = claims.evaluate
				}
				grant, err := evaluate(ctx, input)
				if err == nil {
					serve()
				}
				return grant, err
			},
		},
	}
	if capacity, ok := cfg.Store.(nodehost.CapacityStore); ok {
		config.Capacity = &nodehost.StoreAdmission{Store: capacity, BoxID: admission.BoxID}
	}
	return runners.NativeServices{Config: config, Storage: storage, Close: closeResources}, nil
}

func localProcessHTTP(ctx context.Context, listener net.Listener, handler http.Handler, release func() error) (func(), func() error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	var startOnce, closeOnce sync.Once
	var closeErr error
	start := func() {
		startOnce.Do(func() {
			go func() { done <- wingd.ServeLocalHTTP(ctx, listener, handler, nil, nil) }()
		})
	}
	return start, func() error {
		closeOnce.Do(func() {
			cancel()
			start()
			closeErr = errors.Join(<-done, release())
		})
		return closeErr
	}
}

func localStateSubscription(ctx context.Context, _ string) (nodehost.StateSubscription, error) {
	ctx, cancel := context.WithCancel(ctx)
	changed, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		}
	}()
	return nodehost.StateSubscription{Changed: changed, Close: func() error { cancel(); <-done; return nil }}, nil
}

// safety: a remote base inherits the hosting process's tool environment, which
// can still hold values the run releases only through its local secret registry.
func withoutLocalSecrets(environment, names []string, masker *secrets.Masker) []string {
	held := map[string]bool{}
	for _, name := range names {
		held[procenv.NameKey(name, runtime.GOOS)] = true
	}
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if held[procenv.NameKey(name, runtime.GOOS)] {
			masker.Register(value)
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}
