//go:build unix

package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNativeHostFromIntakeThroughSDKAndPersistedCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := buildNativeSDKFixture(t, ctx, "stepsfixture")
	runner := nodeproto.RunnerIdentity{Name: "host", Type: "agent", Labels: []string{}}
	hello, _ := planNativeSDKFixture(t, ctx, bin, "steps", map[string]string{"set": "dry"}, nodeproto.RunContext{RunID: "describe", Pipeline: "steps"}, runner)
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
		Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost, Env: environment,
		Git:           runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: sha},
		Source:        runspec.Source{Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: "."}},
		RequestedArgs: map[string]string{"set": "dry"},
	}
	f := newStoreOwnerFixture(t, openStore(t), request)
	outputs := nativeStoreOutputRecords(t, ctx, f, request)
	resolved := engine.Resolved{
		Identity:   engine.Identity{Version: "v1.0.0", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: sdkVersion}},
		Executable: bin, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
	}
	engines := &ownerSourceEngine{claimEngine: claimEngine{resolved: resolved}}
	authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: f.Store, Bindings: f.Store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authorities.Close() })
	var mu sync.Mutex
	var logs []string
	processes := &NativeProcesses{
		Authorities: authorities, URL: "http://host",
		Log: func(_ context.Context, run, job string, frame nodeproto.FrameLog) error {
			if run != f.Input.RunID || job != "dry" {
				return errors.New("native log lost its run or job identity")
			}
			mu.Lock()
			logs = append(logs, frame.Message)
			mu.Unlock()
			return nil
		},
	}
	var award store.ReconciledNode
	f.Host.AdoptClaim = func(_ context.Context, claim store.ReconciledNode) error { award = claim; return nil }
	f.Host.Layer = nil
	bind := func(ctx context.Context, input NodeInput) (ProcessGrant, error) {
		if award.Node.Ref.NodeID != input.NodeID || len(award.Bearer) == 0 {
			return ProcessGrant{}, errors.New("native attempt has no adopted award")
		}
		lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256(award.Bearer)}
		parent, err := f.Store.ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
		if err != nil {
			return ProcessGrant{}, err
		}
		principal := parent.Authorities[0].Principal
		binding := principal.Capability.Binding
		binding.Attempt = input.Attempt
		return processes.Delegate(ctx, lookup, capability.DelegateSpec{
			Parent: principal.Authority, Audience: capability.NodeHost, Grants: []capability.Grant{capability.NodeReport},
			Binding: binding, ExpiresAt: principal.Capability.ExpiresAt,
		}, ProcessServices{RunID: input.Accepted.RunID, Job: input.Accepted.Plan.Jobs[0]})
	}
	config := NativeHostConfig{
		Store: f.State, Lease: f.Host.Lease, Snapshots: snapshots, Owner: f.Host,
		Masks: NewMaskRegistry(secrets.NewMasker()), Processes: processes, Router: &serviceRegistrar{},
		Admission:  &LocalAdmission{Daemon: newFakeDaemon()},
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
		Planner:  SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
		Executor: ExecutorDependencies{Bind: bind, PublishOutput: outputs.PublishOutput},
		Evaluation: EvaluationDependencies{
			Bind: func(context.Context, EvaluationInput) (ProcessGrant, error) {
				return ProcessGrant{}, errors.New("unexpected owner evaluation")
			},
			ReadOutput: func(context.Context, store.NodeRef, int64) (json.RawMessage, error) {
				return nil, errors.New("unexpected source output read")
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
		readErr := f.Store.DB().QueryRowContext(ctx, "SELECT failure_json FROM nodes WHERE run_id=? AND node_id='dry'", f.Input.RunID).Scan(&failure)
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
	mu.Lock()
	text := strings.Join(logs, "\n")
	mu.Unlock()
	for _, message := range []string{"real body", "safe ran", "plain ran"} {
		if !strings.Contains(text, message) {
			t.Fatalf("native body missing %q: %s", message, text)
		}
	}
	if processes.computing(f.Input.RunID) || len(authorities.live) != 0 {
		t.Fatal("native host returned with active compute or delegated authority")
	}
	wantLeases := 1
	if role == "owner" {
		wantLeases++
	}
	if live, granted, released := config.Admission.Daemon.(*fakeDaemon).counts(); live != 0 || granted != wantLeases || released != wantLeases {
		t.Fatalf("admission leases=%d/%d/%d, want 0/%d/%d", live, granted, released, wantLeases, wantLeases)
	}
}

func nativeStoreOutputRecords(t *testing.T, ctx context.Context, f *storeHostFixture, request runspec.Request) OutputRecords {
	t.Helper()
	f.Store.SetResultBlobRecords(nil)
	if err := f.Tenant.SetStorageQuota(ctx, store.StorageQuota{}); err != nil {
		t.Fatal(err)
	}
	bytes := &scriptedStorage{onPut: func(ctx context.Context, request blob.PutRequest) (blob.Receipt, error) {
		value, err := io.ReadAll(request.Body)
		if err != nil || outputDigest(value) != request.Digest || int64(len(value)) != request.Size {
			return blob.Receipt{}, errors.Join(blob.ErrIntegrity, err)
		}
		ticket, err := f.Tenant.ReserveBlobUpload(ctx, store.UploadReservationRequest{
			ID: "native-output", Intent: blob.UploadIntent{Identity: request.Identity, Authority: request.Authority, Digest: request.Digest, Size: request.Size}, ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			return blob.Receipt{}, err
		}
		return f.Tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{Authority: request.Authority, TicketID: ticket.ID, Verified: ticket.Blob, ReplicaID: "native-host"})
	}}
	return OutputRecords{Blobs: bytes, Records: f.Tenant.BlobRecords(nil), Bind: func(ctx context.Context, input NodeInput) (JobUpload, error) {
		principal, err := f.Store.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix})
		if err != nil {
			return JobUpload{}, err
		}
		attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Attempt: input.Attempt, Generation: input.Generation}}
		identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: request.Team, RunID: input.Accepted.RunID, Principal: blob.Principal(principal.Capability.Subject.ID), CapabilityRef: input.Authority.CapabilityPrefix, Attempt: attempt}
		authority := blob.Authority{CapabilityRef: identity.CapabilityRef, Principal: identity.Principal, Repo: request.Git.Repository.Key, Attempt: attempt, Ref: blob.RefFacts{SHA: request.Git.SHA}}
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
	ref := store.NodeRef{RunID: f.Input.RunID, NodeID: "dry"}
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
