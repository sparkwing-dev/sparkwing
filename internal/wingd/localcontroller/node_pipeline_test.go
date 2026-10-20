//go:build unix

package localcontroller

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func localAwardPlan() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "first", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}, Artifacts: []string{"result.txt"}},
		{ID: "second", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}, Needs: []string{"first"}, Consumes: []nodeproto.ArtifactInput{{Producer: "first", Into: "received"}}},
	}}
}

func TestLocalAwardProcess(t *testing.T) {
	if !slices.Contains(os.Args, "local-award-process") {
		return
	}
	requests := bufio.NewScanner(os.NewFile(3, "requests"))
	frames := json.NewEncoder(os.NewFile(4, "frames"))
	var binding *nodeproto.ProcessBinding
	for requests.Scan() {
		var request nodeproto.Request
		if err := json.Unmarshal(requests.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		var result any = map[string]any{}
		switch request.Op {
		case nodeproto.OpHello:
			binding = request.Binding
			if binding == nil || binding.JobID == "" || !strings.HasPrefix(binding.Routes.Bearer, capability.NodeHostPrefix) {
				t.Fatal("process did not receive its node-host credential")
			}
			result = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: "v1.2.3"}, Features: []string{}}
		case nodeproto.OpPlan:
			result = localAwardPlan()
		case nodeproto.OpRunStep:
			if binding == nil || request.Job != binding.JobID {
				t.Fatal("process executed another job")
			}
			if request.Job == "first" {
				if err := os.WriteFile("result.txt", []byte("artifact from first process\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			} else {
				body, err := os.ReadFile(filepath.Join("received", "result.txt"))
				if err != nil || string(body) != "artifact from first process\n" {
					t.Fatalf("consumer lost producer artifact: %q %v", body, err)
				}
			}
			fmt.Println("ran " + request.Job)
			result = map[string]any{"outcome": "success", "output": map[string]string{"job": request.Job}}
		}
		if err := frames.Encode(map[string]any{"reply": request.ID, "ok": true, "result": result}); err != nil {
			t.Fatal(err)
		}
	}
	if err := requests.Err(); err != nil {
		t.Fatal(err)
	}
	// safety: testing's PASS line would otherwise enter the SDK log stream after its final flush.
	os.Exit(0)
}

type pipelineListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *pipelineListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipelineListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*pipelineListener) Addr() net.Addr { return &net.TCPAddr{} }

func pipelineHTTP(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	listener := &pipelineListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case listener.connections <- server:
			return client, nil
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, context.Cause(ctx)
		}
	}}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	return &http.Client{Transport: transport}
}

type pipelineEngine struct{ resolved engine.Resolved }

func (p pipelineEngine) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return p.resolved, nil
}

func (p pipelineEngine) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return p.resolved, nil
}

type pipelineAdmission struct{}

func (pipelineAdmission) Acquire(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (nodehost.Lease, error) {
	return pipelineAdmission{}, nil
}

func (pipelineAdmission) Resources() wingwire.HostResources { return wingwire.HostResources{} }

func (pipelineAdmission) Token() string                                             { return "pipeline-test" }
func (pipelineAdmission) Release() error                                            { return nil }
func (pipelineAdmission) Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error { return nil }

func TestLocalControllerTwoJobProcessesUseIndependentAwards(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	var b Binding
	var claimant *Claimant
	c := openFixture(t, func(_ context.Context, binding Binding) (Services, error) {
		b = binding
		services := serviceFixture(binding)
		var err error
		claimant, err = NewClaimant(binding.Store)
		if err != nil {
			t.Fatal(err)
		}
		services.Close = claimant.Close
		objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://controller", SigningKey: make([]byte, 32)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := objects.Close(); err != nil {
				t.Error(err)
			}
		})
		server := controller.New(binding.Store, nil).WithEngineController(binding.Controller).WithDataObjects(objects)
		services.HTTP = server.HTTP()
		services.Policies = server.StorePolicies()
		services.Policies.ClaimProfiles = claimant
		state, err := NewState(binding)
		services.State = state
		return services, err
	})
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	peer, err := b.Store.AsOperator().ReadAuthentication(ctx, capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: claimant.subject}}, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(b.Home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := snapshots.Capture(ctx, []string{"PATH=" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "awards", Mode: runspec.IntakeExecute, Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost, Env: environment, EnvironmentSource: runspec.EnvironmentCaptured, Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.com/repo"}, SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Source: runspec.Source{Ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Location: runspec.SourceLocation{LocalCheckout: t.TempDir(), PipelineDir: "."}}}
	admitted, err := b.Tenant.AdmitRun(ctx, store.IntakeRequest{Authority: peer.Authorities[0].Principal.Authority, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := claimant.Bearer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := b.Store.AsOperator().LookupCapability(ctx, sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	award, err := b.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: root.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: admitted.RunID}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := b.Store.AsOperator().LookupCapability(ctx, sha256.Sum256(award.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	options := client.EngineOptions{URL: "http://controller", HTTP: pipelineHTTP(t, c.HTTP().API), Bearer: func(context.Context) (string, error) { return string(award.Bearer), nil }}
	private, err := client.NewLocalHostClient(options, admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := private.Close(); err != nil {
			t.Error(err)
		}
	})
	remote, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodehost.NewRemoteHostStore(remote, private, principal.Capability.UnitID, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	state.Finish = private.FinishRunAndUnit
	actor := store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Owner: award.Owner}
	plan := localAwardPlan()
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selected := engine.Resolved{Identity: engine.Identity{Version: "v1.2.3", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}}, Executable: executable, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Tenant.AcceptPlan(ctx, store.PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Engine: selected.Identity, Env: environment, Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	services, err := nodehost.NewNativeServices(ctx, nodehost.WithClient(nodehost.NativeConfig{Home: b.Home, Admission: nodehost.ComposeNativeAdmission(nil, "", &nodehost.LocalAdmission{Daemon: pipelineAdmission{}}, true), Local: func(store.NodeRow) bool { return true }, Store: state, Parent: private, Bindings: private, Principal: principal, Refresh: private.Principal, Renew: b.Store.AsOperator().RenewUnit, Source: source.Options{Engines: pipelineEngine{selected}, Base: procenv.Base{Local: true}}}, private))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	adopt := services.Config.Owner.AdoptClaim
	services.Config.Owner.AdoptClaim = func(ctx context.Context, claim store.ReconciledNode) error {
		if err := private.AdoptClaim(ctx, claim); err != nil {
			return err
		}
		actor := store.Actor{Kind: store.ActorHolder, CapabilityPrefix: claim.Claim.CapabilityPrefix, Holder: claim.Claim.Holder, Generation: claim.Claim.Generation}
		beat, err := state.RenewNodeClaim(ctx, claim.Node.Ref, actor)
		if err != nil {
			return fmt.Errorf("renew unstarted local job: %w", err)
		}
		row, err := private.ReadNode(ctx, claim.Node.Ref)
		if err != nil || row.Status != store.StatusPending || row.Attempt != 0 || row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.Equal(beat.ExpiresAt) {
			return fmt.Errorf("unstarted local renewal changed claim: %+v: %w", row, err)
		}
		return adopt(ctx, claim)
	}
	bind := services.Config.Executor.Bind
	if grant, err := bind(ctx, nodehost.NodeInput{Accepted: nodehost.AcceptedRun{RunID: admitted.RunID}, NodeID: "first", Generation: actor.Owner.Generation, Authority: nodehost.ClaimAuthority{CapabilityPrefix: principal.Capability.Prefix}}); err == nil || grant.Binding.Routes.Bearer != "" {
		t.Fatal("owner authority substituted for a missing node award")
	}
	var mu sync.Mutex
	parents := map[string]string{}
	services.Config.Executor.Bind = func(ctx context.Context, input nodehost.NodeInput) (nodehost.ProcessGrant, error) {
		grant, err := bind(ctx, input)
		if err != nil {
			return grant, err
		}
		snapshot, err := services.Config.Processes.Authorities.ReadAuthentication(ctx, capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(grant.Binding.Routes.Bearer))}, capability.RequestEvidence{})
		if err != nil {
			return grant, err
		}
		process := snapshot.Authorities[0].Principal.Capability
		if process.Parent == nil || process.Parent.ID == principal.Capability.Prefix || process.Parent.ID != input.Authority.CapabilityPrefix || process.Binding.Kind != capability.BindingNode || process.Binding.RunID != input.Accepted.RunID || process.Binding.NodeID != input.NodeID || process.Binding.Generation != input.Generation || process.Binding.Attempt != input.Attempt || process.Binding.Holder != input.Authority.Holder {
			return grant, errors.New("process substituted owner or foreign node authority")
		}
		bound, err := services.Config.Processes.Authorities.Resolve(ctx, capability.Authorized{Principal: snapshot.Authorities[0].Principal, Binding: process.Binding})
		if err != nil || bound == nil || (bound.Secret == nil || bound.Output == nil || bound.Latest == nil || bound.OIDC == nil || bound.Annotation == nil || bound.Summary == nil || bound.StartChild == nil || bound.AwaitChild == nil) || bound.Job.ID != input.NodeID {
			return grant, fmt.Errorf("process services were incomplete at construction: %w", err)
		}
		mu.Lock()
		parents[input.NodeID] = process.Parent.ID
		mu.Unlock()
		return grant, nil
	}
	host, _, _, err := nodehost.NewNativeHost(services.Config)
	if err != nil {
		t.Fatal(err)
	}
	host.Acquire = func(context.Context, nodehost.RunInput) (*source.Entry, error) {
		return &source.Entry{Engine: selected, Directory: t.TempDir(), Command: []string{executable, "-test.run=^TestLocalAwardProcess$", "--", "local-award-process"}}, nil
	}
	requestBody, err := json.Marshal(admitted.Request.Request)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := host.Run(ctx, nodehost.RunInput{RunID: admitted.RunID, Owner: actor, RequestHash: string(admitted.Request.Hash), SealedRequest: requestBody, Storage: services.Storage, GrantedRunner: nodeproto.RunnerIdentity{Name: principal.Capability.Subject.ID, Type: "coordinator", Labels: []string{}}})
	if err != nil || completion.Outcome != "success" {
		rows, readErr := b.Tenant.ReadSnapshot(ctx, admitted.RunID)
		for _, row := range rows.Nodes {
			var failure []byte
			readFailure := stateSQL(t, b).QueryRowContext(ctx, "SELECT failure_json FROM nodes WHERE run_id=? AND node_id=?", admitted.RunID, row.Ref.NodeID).Scan(&failure)
			t.Logf("job %s: %s read=%v", row.Ref.NodeID, failure, readFailure)
		}
		t.Fatalf("local pipeline: %+v: %v read=%v", completion, err, readErr)
	}
	if len(parents) != 2 || parents["first"] == parents["second"] {
		t.Fatalf("jobs did not receive independent awards: %v", parents)
	}
	snapshot, err := private.ReadSnapshot(ctx, admitted.RunID)
	if err == nil || snapshot.Run.ID != "" {
		t.Fatal("completed owner retained private authority")
	}
	stored, err := b.Tenant.ReadSnapshot(ctx, admitted.RunID)
	if err != nil || stored.Run.State != store.RunSuccess || len(stored.Nodes) != 2 {
		t.Fatalf("persisted pipeline: %+v: %v", stored, err)
	}
	for _, row := range stored.Nodes {
		if row.Ref.NodeID == "first" && row.ManifestRecord == nil {
			t.Fatalf("producer artifact manifest was not committed: %+v", row)
		}
		if row.OutputRecord == nil {
			t.Fatalf("job output was not committed: %+v", row)
		}
		if row.Outcome != store.NodeSuccess || row.Attempt != 1 || row.StateActor.Authority.ID != parents[row.Ref.NodeID] {
			t.Fatalf("job used another award: %+v", row)
		}
	}
}
