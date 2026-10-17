package controller_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

const burstSDKPipelineName = "burst-execution-proof"

type (
	burstSDKPipeline struct{}
	burstSDKSecrets  struct {
		Token string `sw:"TOKEN,required"`
	}
)

func (burstSDKPipeline) Secrets() any { return &burstSDKSecrets{} }

func (burstSDKPipeline) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(plan, "job", func(ctx context.Context) error {
		value := sw.PipelineSecrets[burstSDKSecrets](ctx).Token
		if value == "" || sw.Runner(ctx) == nil || sw.Runner(ctx).Name == "" {
			return errors.New("SDK lost requester secrets or granted runner")
		}
		sw.Info(ctx, "secret=%s encoded=%s", value, base64.StdEncoding.EncodeToString([]byte(value)))
		if err := os.MkdirAll("modules", 0o700); err != nil {
			return err
		}
		if err := os.WriteFile("modules/dependency", []byte("cached burst dependency\n"), 0o600); err != nil {
			return err
		}
		return os.WriteFile("burst-result.txt", []byte("artifact from burst SDK\n"), 0o640)
	}).Artifacts("burst-result.txt").Cache(sw.Dir("modules", sw.KeyFromFile("source-marker.txt")))
	return nil
}

func init() {
	sw.Register(burstSDKPipelineName, func() sw.Pipeline[sw.NoInputs] { return burstSDKPipeline{} })
}

func TestBurstSDKExecutesHerePersonalAndCloudAwards(t *testing.T) {
	_, plan, err := sdkserve.DefaultOperations.Plan(t.Context(), burstSDKPipelineName, map[string]string{}, nodeproto.RunContext{Pipeline: burstSDKPipelineName})
	if err != nil {
		t.Fatal(err)
	}
	identity := engine.Identity{Version: "v1.2.3", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}}
	for _, placement := range []string{"here", "personal", "cloud"} {
		t.Run(placement, func(t *testing.T) {
			controller.BurstExecutionAcceptance(t, placement, controller.BurstExecutionProof{
				Pipeline: burstSDKPipelineName, Plan: plan, Engine: identity,
				Execute: func(options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
					executeBurstSDK(t, options, award, owner)
				},
			})
		})
	}
}

func executeBurstSDK(t *testing.T, options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
	executeBurstSDKProcess(t, options, award, owner, nil)
}

type burstSDKNativeExecution struct {
	entry      *source.Entry
	engines    *engine.Resolver
	serviceURL func(http.Handler) string
	source     *source.Options
	acquire    func(context.Context, source.Spec, source.Options) (*source.Entry, error)
}

func executeBurstSDKProcess(t *testing.T, options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller, native *burstSDKNativeExecution) {
	t.Helper()
	ctx := t.Context()
	private, err := client.NewNodeHostClient(options, award.RunID, award.JobID)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	wire, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	var processes nodehost.NativeProcesses
	state, err := nodehost.NewRemoteHostStore(wire, private, award.UnitID, func() bool { return processes.Computing(award.RunID) })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.ReadSnapshot(ctx, award.RunID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot.Run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(snapshot.Run.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	accepted := nodehost.AcceptedRun{RunID: award.RunID, Spec: raw, SpecHash: string(snapshot.Run.Spec.Hash), Plan: plan, PlanHash: snapshot.Run.PlanHash}
	row, err := state.ReadNode(ctx, store.NodeRef{RunID: award.RunID, NodeID: award.JobID})
	if err != nil {
		t.Fatal(err)
	}
	selected := engine.Resolved{Identity: *award.Engine, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	input, err := nodehost.NewNodeInput(accepted, row, selected, award.Runner)
	if err != nil {
		t.Fatal(err)
	}
	input.Entry = &source.Entry{Engine: selected, Directory: t.TempDir()}
	if native != nil {
		input.Entry = native.entry
	} else {
		t.Chdir(input.Entry.Directory)
	}
	if input.Entry != nil {
		if err := os.WriteFile(filepath.Join(input.Entry.Directory, "source-marker.txt"), []byte("uploaded edited source\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	masker := secrets.NewMasker()
	masks := nodehost.NewMaskRegistry(masker)
	authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: private, Bindings: private})
	if err != nil {
		t.Fatal(err)
	}
	defer authorities.Close()
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	if err := nodehost.RegisterProcessServices(router, authorities.Resolve); err != nil {
		t.Fatal(err)
	}
	processes = nodehost.NativeProcesses{Authorities: authorities, Masks: masks, URL: "http://127.0.0.1:1"}
	var logs burstSDKLogs
	if native != nil {
		if native.source == nil {
			processes.URL = native.serviceURL(router)
		}
		processes.Session.Environments = nodehost.SDKEnvironment{Store: state, Masks: masks}
		if native.source != nil {
			native.source.Base.Masker = masker
			processes.Session.Environments.Base = native.source.Base
		}
	}
	processes.Log = func(_ context.Context, _, _ string, frame nodeproto.FrameLog) error {
		logs.WriteString(frame.Message)
		logs.Write(frame.Attrs)
		return nil
	}
	closed := false
	start := func(_ context.Context, input nodehost.NodeInput, _ nodehost.ExecutorActivity) (nodehost.ExecutorProcess, error) {
		return &burstSDKProcess{runner: input.GrantedRunner, identity: selected.Identity, services: &burstSDKServices{handler: router, bearer: input.RouteBinding.Routes.Bearer, masker: masker, logs: &logs}, closed: &closed}, nil
	}
	if native != nil {
		start = processes.Start
	}
	artifacts := nodehost.ArtifactRecords{Bind: func(_ context.Context, input nodehost.NodeInput) (nodehost.JobUpload, error) {
		authority := input.Storage.Authority
		return nodehost.JobUpload{Identity: authority.Upload.Value, Authority: authority, Key: blob.ResultRecordKey(authority.Attempt.Value, authority.Repo, authority.Ref.Scope, burstSDKPipelineName, blob.KindManifest)}, nil
	}}
	caches := nodehost.DepcacheRecords{Bind: func(ctx context.Context, input nodehost.NodeInput, key blob.DepcacheKey) (nodehost.JobUpload, error) {
		upload, err := artifacts.Bind(ctx, input)
		upload.Key.Kind, upload.Key.Name, upload.Key.Scope.Key = blob.KindDepcache, key.Name, key.Key
		return upload, err
	}}
	destination := t.TempDir()
	executor, err := nodehost.NewNodeExecutor(nodehost.ExecutorDependencies{
		BindStorage: func(ctx context.Context, _ nodehost.NodeInput) (nodehost.StorageBinding, error) {
			principal, err := private.Principal(ctx)
			if err != nil {
				return nodehost.StorageBinding{}, err
			}
			storage, err := private.BindStorage(ctx, principal)
			return nodehost.StorageBinding{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority}, err
		},
		PrepareLayer: func(ctx context.Context, layer nodehost.LayerSpec, input nodehost.NodeInput, job nodeproto.JobSpec) (nodehost.ExecutorFinalizer, error) {
			if layer.Name == "caches" {
				return caches.Prepare(ctx, layer, input, job)
			}
			finish, err := artifacts.Prepare(ctx, layer, input, job)
			if err != nil || finish == nil {
				return finish, err
			}
			return func(ctx context.Context, current nodehost.NodeInput, result *store.NodeResult) error {
				if err := finish(ctx, current, result); err != nil {
					return err
				}
				if result.ManifestRecord == nil {
					return errors.New("successful burst SDK did not publish declared artifacts")
				}
				reader := nodehost.ArtifactRecords{Blobs: current.Storage.Bytes, Records: current.Storage.Records}
				count, err := reader.Stage(ctx, blob.RecordLookup{Key: *result.ManifestRecord, Authority: current.Storage.Authority}, destination, "received")
				if err != nil {
					return fmt.Errorf("restore burst artifact: %w", err)
				}
				if count != 1 {
					return fmt.Errorf("restored %d burst artifacts, want one", count)
				}
				body, err := os.ReadFile(filepath.Join(destination, "received", "burst-result.txt"))
				if err != nil || string(body) != "artifact from burst SDK\n" {
					return errors.New("restored burst artifact differs from the SDK output")
				}
				workspace := current.Entry.CheckoutDirectory()
				if workspace == "" {
					workspace = current.Entry.Directory
				}
				directory := filepath.Join(workspace, "modules")
				if err := os.RemoveAll(directory); err != nil {
					return err
				}
				if _, err := caches.Prepare(ctx, nodehost.LayerSpec{Name: "caches"}, current, job); err != nil {
					return err
				}
				body, err = os.ReadFile(filepath.Join(directory, "dependency"))
				if err != nil || string(body) != "cached burst dependency\n" {
					return errors.New("restored native cache differs from SDK output")
				}
				return nil
			}, nil
		},
		Store: state, Mask: masker.Mask, Masks: masks,
		Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementRunnerHosted, Secrets: masks},
		Log: func(_ context.Context, _ nodehost.NodeInput, frame nodeproto.FrameLog) error {
			logs.WriteString(frame.Message)
			logs.Write(frame.Attrs)
			return nil
		},
		EnsureEngine: func(ctx context.Context, identity engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
			if native != nil && native.engines != nil {
				return native.engines.Ensure(ctx, identity, target)
			}
			return selected, nil
		},
		Bind: func(ctx context.Context, input nodehost.NodeInput) (nodehost.ProcessGrant, error) {
			principal, err := private.Principal(ctx)
			if err != nil {
				return nodehost.ProcessGrant{}, err
			}
			services, err := private.ProcessClient(ctx, principal, award.JobID)
			if err != nil {
				return nodehost.ProcessGrant{}, err
			}
			return processes.Delegate(ctx, capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(award.Capability))}, capability.DelegateSpec{
				Parent: principal.Authority, Audience: capability.NodeHost, Grants: []capability.Grant{capability.NodeReport, capability.SecretsRelease}, Binding: principal.Capability.Binding, Repositories: principal.Capability.Repositories, ExpiresAt: principal.Capability.ExpiresAt,
			}, nodehost.ProcessServices{RunID: award.RunID, Job: input.Accepted.Plan.Jobs[0], Secrets: []string{"TOKEN"}, Secret: func(ctx context.Context, _ capability.Authorized, name string) (string, bool, error) {
				return services.Secret(ctx, name)
			}})
		},
		Start: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	execute := executor.Execute
	if native != nil && native.source != nil {
		host := &nodehost.ClaimedNodeHost{
			Store: state, Lease: lease.Keeper{Lease: time.Minute, Interval: 20 * time.Second},
			Source: *native.source, Executor: executor, Mask: masker.Mask, Masks: masks,
			Acquire: func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
				entry, err := native.acquire(ctx, spec, options)
				if err == nil {
					native.entry = entry
					processes.URL = native.serviceURL(router)
				}
				return entry, err
			},
		}
		input.Entry = nil
		execute = host.Execute
	}
	if err := execute(ctx, input); err != nil {
		t.Fatalf("SDK execution: %v; logs: %s", err, logs.String())
	}
	_, err = state.ReadNode(ctx, row.Ref)
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.Unauthenticated || native == nil && !closed || processes.Computing(award.RunID) {
		t.Fatalf("completed SDK retained claim authority: closed=%v error=%v", closed, err)
	}
	if !strings.Contains(logs.String(), "secret=*** encoded=***") || strings.Contains(logs.String(), "requesting-machine-only-secret") {
		observed, readErr := owner.ReadSnapshot(ctx, award.RunID)
		t.Fatalf("SDK masking: %s; observed=%+v read=%v", logs.String(), observed, readErr)
	}
}

type burstSDKServices struct {
	sdkserve.Services
	handler http.Handler
	bearer  string
	masker  *secrets.Masker
	logs    *burstSDKLogs
}

type burstSDKLogs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *burstSDKLogs) WriteString(value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(value)
}

func (l *burstSDKLogs) Write(value []byte) {
	l.WriteString(string(value))
}

func (l *burstSDKLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (s *burstSDKServices) Secret(ctx context.Context, name string) (string, bool, error) {
	request := httptest.NewRequest(http.MethodGet, "/node/v1/secrets/"+name, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+s.bearer)
	response := httptest.NewRecorder()
	s.handler.ServeHTTP(response, request)
	var value struct {
		Value  string `json:"value"`
		Masked bool   `json:"masked"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &value) != nil {
		return "", false, fmt.Errorf("SDK secret service: HTTP %d", response.Code)
	}
	if !value.Masked || s.masker.Mask(value.Value) != "***" {
		return "", false, errors.New("SDK received secret before mask registration")
	}
	return value.Value, value.Masked, nil
}

func (s *burstSDKServices) Emit(frame nodeproto.FrameLog) {
	s.logs.WriteString(s.masker.Mask(frame.Message))
}

type burstSDKProcess struct {
	runner   nodeproto.RunnerIdentity
	identity engine.Identity
	services *burstSDKServices
	plan     any
	run      nodeproto.RunContext
	closed   *bool
}

func (p *burstSDKProcess) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	ops := sdkserve.DefaultOperations
	ctx = ops.Context(ctx, p.run, p.runner, p.services)
	var result any
	var err error
	switch request.Op {
	case nodeproto.OpHello:
		result = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: p.identity.Pin.Version}, Features: []string{}}
	case nodeproto.OpPlan:
		p.run = *request.Run
		var plan nodeproto.PlanDoc
		p.plan, plan, err = ops.Plan(ctx, request.Pipeline, request.Args, p.run)
		result = plan
	case nodeproto.OpRunStep:
		result, err = ops.RunStep(ctx, p.plan, request)
	default:
		err = fmt.Errorf("unexpected SDK operation: %s", request.Op)
	}
	raw, marshalErr := json.Marshal(result)
	return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, errors.Join(err, marshalErr)
}

func (p *burstSDKProcess) Close() error                { *p.closed = true; return nil }
func (*burstSDKProcess) Cleanup(context.Context) error { return nil }
