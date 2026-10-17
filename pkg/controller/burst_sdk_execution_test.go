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
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
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
		return nil
	})
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
	serviceURL func(http.Handler) string
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
	state, err := nodehost.NewRemoteHostStore(wire, private, award.UnitID, func() bool { return true })
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
	input.Entry = &source.Entry{Engine: selected}
	if native != nil {
		input.Entry = native.entry
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
	processes := nodehost.NativeProcesses{Authorities: authorities, Masks: masks, URL: "http://127.0.0.1:1"}
	var logs burstSDKLogs
	if native != nil {
		processes.URL = native.serviceURL(router)
		processes.Session.Environments = nodehost.SDKEnvironment{Store: state, Masks: masks}
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
	executor, err := nodehost.NewNodeExecutor(nodehost.ExecutorDependencies{
		Store: state, Mask: masker.Mask, Masks: masks,
		Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementRunnerHosted, Secrets: masks},
		Log: func(_ context.Context, _ nodehost.NodeInput, frame nodeproto.FrameLog) error {
			logs.WriteString(frame.Message)
			logs.Write(frame.Attrs)
			return nil
		},
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
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
	if err := executor.Execute(ctx, input); err != nil {
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
