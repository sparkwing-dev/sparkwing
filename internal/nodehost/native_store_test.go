//go:build unix

package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestNativeHostFromIntakeThroughSDKAndPersistedCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := buildNativeSDKFixture(t, ctx, "stepsfixture")
	runner := nodeproto.RunnerIdentity{Name: "host", Type: "agent", Labels: []string{}}
	hello, _ := planNativeSDKFixture(t, ctx, bin, "steps", map[string]string{"set": "output"}, nodeproto.RunContext{RunID: "describe", Pipeline: "steps"}, runner)
	for _, role := range []string{"owner", "claimed"} {
		t.Run(role, func(t *testing.T) { nativeStoreHost(t, ctx, bin, hello.SDK.Version, role) })
	}
}

func nativeStoreHost(t *testing.T, ctx context.Context, bin, sdkVersion, role string) {
	t.Helper()
	root, sha := hostSourceCheckout(t)
	snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	environment, err := snapshots.Capture(ctx, []string{"PATH=" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	request := runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "steps", Mode: runspec.IntakeExecute,
		Origin: runspec.OriginLocal, EnvironmentSource: runspec.EnvironmentCaptured, Dispatch: runspec.DispatchHost, Env: environment,
		Git:           runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: sha},
		Source:        runspec.Source{Ref: sha, WorkingTree: true, Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: "."}},
		RequestedArgs: map[string]string{"set": "output"},
	}
	objects, dataDirectory := nativeStoreObjects(t)
	database := openStore(t, store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{ResultBlobs: storage.BlobManifests{Objects: objects}}, nil
	}))
	f := newStoreOwnerFixture(t, database, request)
	outputs := nativeStoreOutputRecords(t, ctx, f, request, objects)
	resolved := engine.Resolved{
		Identity:   engine.Identity{Version: "v1.0.0", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: sdkVersion}},
		Executable: bin, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
	}
	engines := &ownerSourceEngine{claimEngine: claimEngine{resolved: resolved}}
	authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: f.Store.AsOperator(), Bindings: f.Store.AsOperator()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authorities.Close() })
	var mu sync.Mutex
	var logs []string
	processes := &NativeProcesses{
		Authorities: authorities, URL: "http://host",
		Log: func(_ context.Context, run, job string, frame nodeproto.FrameLog) error {
			if run != f.Input.RunID || job != "output" {
				return errors.New("native log lost its run or job identity")
			}
			mu.Lock()
			logs = append(logs, frame.Message)
			mu.Unlock()
			return nil
		},
	}
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	var award store.ReconciledNode
	f.Host.AdoptClaim = func(_ context.Context, claim store.ReconciledNode) error { award = claim; return nil }
	f.Host.DebugPolicy, f.Host.SkipPolicy, f.Host.ApprovalPolicy, f.Host.MemoPolicy, f.Host.AdmissionPolicy = nil, nil, nil, nil, nil
	bind := func(ctx context.Context, input NodeInput) (ProcessGrant, error) {
		if award.Node.Ref.NodeID != input.NodeID || len(award.Bearer) == 0 {
			return ProcessGrant{}, errors.New("native attempt has no adopted award")
		}
		lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256(award.Bearer)}
		parent, err := f.Store.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
		if err != nil {
			return ProcessGrant{}, err
		}
		principal := parent.Authorities[0].Principal
		binding := principal.Capability.Binding
		binding.Attempt = input.Attempt
		grant, err := processes.Delegate(ctx, lookup, capability.DelegateSpec{
			Parent: principal.Authority, Audience: capability.NodeHost, Grants: []capability.Grant{capability.NodeReport},
			Binding: binding, Repositories: principal.Capability.Repositories, ExpiresAt: principal.Capability.ExpiresAt,
		}, ProcessServices{RunID: input.Accepted.RunID, Job: input.Accepted.Plan.Jobs[0]})
		if err != nil {
			return grant, err
		}
		request := httptest.NewRequest(http.MethodPost, "/node/v1/masks", strings.NewReader(`{"value":"native-route-secret"}`)).WithContext(ctx)
		request.Header.Set("Authorization", "Bearer "+grant.Binding.Routes.Bearer)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || processes.Masks.mask("native-route-secret") != "***" {
			return grant, errors.New("native process could not register its mask through persisted authorization")
		}
		return grant, nil
	}
	config := NativeHostConfig{
		Store: f.State, Lease: f.Host.Lease, Snapshots: snapshots, Owner: ownerOperationsFixture(f.Host),
		Masks: NewMaskRegistry(secrets.NewMasker()), Processes: processes, Router: router,
		Admission:  ComposeNativeAdmission(f.Tenant, "", &LocalAdmission{Daemon: newFakeDaemon()}, false),
		Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal},
		Source: source.Options{
			Engines: engines, Target: resolved.Target, SDK: engine.GoSDK, Base: procenv.Base{Local: true},
			ReadPin: func(string, engine.SDKKind) (engine.Pin, error) { return resolved.Identity.Pin, nil },
			InstallSDK: func(_ context.Context, selected engine.Resolved, input engine.SDKInstallRequest) (engine.SDKCommand, error) {
				if !reflect.DeepEqual(selected, resolved) || filepath.Join(input.Directory, input.PipelineDir) != root {
					return engine.SDKCommand{}, errors.New("installer lost the selected engine or sealed checkout")
				}
				return engine.SDKCommand{Directory: root, Command: []string{bin}}, nil
			},
		},
		PlanningLog: func(context.Context, nodeproto.FrameLog) error { return nil },
		Executor:    ExecutionOperations{Bind: bind, PublishOutput: outputs.PublishOutput},
		ReadOutput: func(context.Context, controllerwire.NodeRef, int64) (json.RawMessage, error) {
			return nil, errors.New("unexpected source output read")
		},
		Evaluation: EvaluationOperations{
			Bind: func(context.Context, EvaluationInput) (ProcessGrant, error) {
				return ProcessGrant{}, errors.New("unexpected owner evaluation")
			},
		},
	}
	host, claimed, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	if host.Executor != claimed.Executor {
		t.Fatal("owner and claimed work use different executors")
	}
	if role == "claimed" {
		var input NodeInput
		input, award = nativeStoreClaim(t, ctx, f, host, resolved)
		if err := claimed.Execute(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	completion, err := host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		var failure []byte
		readErr := teststore.SQL(t, f.Store).QueryRowContext(ctx, "SELECT failure_json FROM nodes WHERE run_id=? AND node_id='output'", f.Input.RunID).Scan(&failure)
		t.Fatalf("completion=%+v error=%v failure=%s read=%v logs=%v", completion, err, failure, readErr, logs)
	}
	snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Input.RunID)
	if err != nil || snapshot.Run.State != store.RunSuccess || snapshot.Run.Spec == nil || len(snapshot.Nodes) != 1 {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}
	row := snapshot.Nodes[0]
	if row.Outcome != store.NodeSuccess || row.Attempt != 1 || row.Generation != 1 || row.ClaimedBy != "" || snapshot.Run.Spec.Resolved.Engine != resolved.Identity {
		t.Fatalf("persisted completion lost seals or execution identity: %+v", row)
	}
	const output = `"result-from-a"`
	key, err := blob.ObjectKey(request.Team, outputDigest([]byte(output)))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(dataDirectory, filepath.FromSlash(key)))
	if err != nil || string(stored) != output {
		t.Fatalf("persisted output=%s error=%v", stored, err)
	}
	pending, err := os.ReadDir(filepath.Join(dataDirectory, "teams", request.Team, "pending"))
	if err != nil || len(pending) != 0 {
		t.Fatalf("unsettled upload files=%v error=%v", pending, err)
	}
	mu.Lock()
	text := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(text, "b saw from-a after 2 checks") {
		t.Fatalf("native body lost its dependency output or group settlement: %s", text)
	}
	if processes.Computing(f.Input.RunID) || len(authorities.live) != 0 {
		t.Fatal("native host returned with active compute or delegated authority")
	}
	wantLeases := 2
	if live, granted, released := config.Admission.compute.Daemon.(*fakeDaemon).counts(); live != 0 || granted != wantLeases || released != wantLeases {
		t.Fatalf("admission leases=%d/%d/%d, want 0/%d/%d", live, granted, released, wantLeases, wantLeases)
	}
}

func nativeStoreObjects(t *testing.T) (*blob.Filesystem, string) {
	t.Helper()
	directory := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: directory, Endpoint: "http://host", SigningKey: bytes.Repeat([]byte{7}, sha256.Size)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	return objects, directory
}

func nativeStoreOutputRecords(t *testing.T, ctx context.Context, f *storeHostFixture, request runspec.Request, objects *blob.Filesystem) OutputRecords {
	t.Helper()
	if err := f.Tenant.SetStorageQuota(ctx, store.StorageQuota{}); err != nil {
		t.Fatal(err)
	}
	transfer := blob.TransferClient{Transport: sourceTransfer(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		objects.ServeHTTP(response, request)
		return response.Result(), nil
	})}
	signer := scriptedDataSigner{onUpload: func(ctx context.Context, request blob.UploadIntent) (blob.UploadTicket, error) {
		ticket, err := f.Tenant.ReserveBlobUpload(ctx, store.UploadReservationRequest{
			ID: "native-output", Intent: request, ExpiresAt: time.Now().Add(time.Minute),
		})
		if err != nil {
			return blob.UploadTicket{}, err
		}
		signed, err := objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodPut, Key: ticket.StagingKey, Size: ticket.Blob.Size, SHA256: string(ticket.Blob.Digest), ExpiresAt: ticket.ExpiresAt})
		if err != nil {
			return blob.UploadTicket{}, err
		}
		return blob.UploadTicket{ID: ticket.ID, Transfer: blob.SignedTransfer{URL: signed.URL, Headers: signed.Headers, Size: signed.Size, Digest: blob.Digest(signed.SHA256), ExpiresAt: signed.ExpiresAt}}, nil
	}, onCommit: func(ctx context.Context, request blob.CommitUpload) (blob.Receipt, error) {
		ticket, err := f.Tenant.LookupBlobUpload(ctx, request.Authority, request.TicketID)
		if err != nil {
			return blob.Receipt{}, err
		}
		if err := objects.CommitObject(ctx, ticket.StagingKey, ticket.Blob); err != nil {
			return blob.Receipt{}, err
		}
		receipt, err := f.Tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{Authority: request.Authority, TicketID: ticket.ID, Verified: ticket.Blob, ReplicaID: "native-host"})
		if err != nil {
			return blob.Receipt{}, err
		}
		return receipt, objects.DeleteObject(ctx, ticket.StagingKey)
	}}
	backend := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer, HTTPClient: transfer})
	return OutputRecords{Blobs: backend, Records: f.Tenant.BlobRecords(nil), Bind: func(ctx context.Context, input NodeInput) (JobUpload, error) {
		principal, err := f.Store.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix})
		if err != nil {
			return JobUpload{}, err
		}
		attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Attempt: input.Attempt, Generation: input.Generation}}
		identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: request.Team, RunID: input.Accepted.RunID, Principal: blob.Principal(principal.Capability.Subject.ID), CapabilityRef: input.Authority.CapabilityPrefix, Attempt: attempt}
		run, err := f.Tenant.ReadRun(ctx, input.Accepted.RunID)
		if err != nil {
			return JobUpload{}, err
		}
		authority := blob.Authority{CapabilityRef: identity.CapabilityRef, Principal: identity.Principal, Repo: request.Git.Repository.Key, Attempt: attempt, Ref: blob.RefFacts{Scope: blob.ScopeFor(run.Request.Provenance), SHA: request.Git.SHA}}
		return JobUpload{Identity: identity, Authority: authority, Key: blob.ResultRecordKey(attempt.Value, authority.Repo, authority.Ref.Scope, request.Pipeline, blob.KindOutput)}, nil
	}}
}

func nativeStoreClaim(t *testing.T, ctx context.Context, f *storeHostFixture, host *OwnerHost, resolved engine.Resolved) (NodeInput, store.ReconciledNode) {
	t.Helper()
	row, err := f.Tenant.ReadRun(ctx, f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := host.acquire(ctx, f.Input, row)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := host.accept(ctx, f.Input, row, entry)
	if err = errors.Join(err, entry.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Tenant.TransitionRun(ctx, store.RunTransition{RunID: f.Input.RunID, To: store.RunRunning, Actor: f.Actor, Cause: store.RunStart}); err != nil {
		t.Fatal(err)
	}
	ref := controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: "output"}
	if _, err := f.State.TransitionNode(ctx, store.NodeTransition{Ref: ref, To: store.NodeReady, Actor: f.Actor, Cause: store.CauseRelease}); err != nil {
		t.Fatal(err)
	}
	award, err := f.State.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewNodeInput(accepted, award.Node, resolved, f.Input.GrantedRunner)
	if err != nil {
		t.Fatal(err)
	}
	return input, award
}
