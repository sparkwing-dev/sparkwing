//go:build unix

package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeExecutorMemoHitUsesClaimAndReleasesAdmission(t *testing.T) {
	ctx := t.Context()
	objects, directory := nativeStoreObjects(t)
	database := openStore(t, store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{ResultBlobs: storage.BlobManifests{Objects: objects}}, nil
	}))
	job := nodeproto.JobSpec{ID: "output", Steps: []nodeproto.StepSpec{{ID: "body"}}, ResultStep: "body", Memo: &nodeproto.MemoSpec{}, Closures: nodeproto.ClosureSpec{MemoKey: "memo"}}
	job.Concurrency = []nodeproto.ConcurrencySpec{{Group: "memo-job", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}}
	job.Artifacts = []string{"artifact.txt"}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)}}
	bodies := 0
	for run := range 2 {
		f := newStoreHostFixtureForRequest(t, database, plan, request)
		outputs := nativeStoreOutputRecords(t, ctx, f, request, objects)
		bytesBackend := &scriptedStorage{onPut: outputs.Blobs.Put, onGet: func(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
			key, err := blob.ObjectKey(request.Authority.Attempt.Value.Team, request.Digest)
			if err != nil {
				return nil, err
			}
			body, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(key)))
			if err != nil {
				return nil, err
			}
			return &outputReader{Reader: bytes.NewReader(body)}, nil
		}}
		ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(job.ID)}
		if _, err := f.State.TransitionNode(ctx, store.ReadyNode{Ref: ref, Actor: f.Actor}); err != nil {
			t.Fatal(err)
		}
		award, err := f.State.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
		if err != nil {
			t.Fatal(err)
		}
		var spec runspec.Spec
		if err := json.Unmarshal(f.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		selected := engine.Resolved{Identity: spec.Resolved.Engine}
		input, err := NewNodeInput(f.Accepted, award.Node, selected, f.Input.GrantedRunner)
		if err != nil {
			t.Fatal(err)
		}
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "artifact.txt"), []byte("memo artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
		input.Entry = &source.Entry{Engine: selected, Directory: workspace}
		artifacts := ArtifactRecords{Bind: func(ctx context.Context, input NodeInput) (JobUpload, error) {
			upload, err := outputs.Bind(ctx, input)
			upload.Key.Kind = blob.KindManifest
			return upload, err
		}}
		daemon := newFakeDaemon()
		var started []NodeInput
		host, err := NewNodeExecutor(ExecutorDependencies{
			Store: f.State, Mask: func(s string) string { return s }, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
			EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return selected, nil
			},
			BindStorage: func(ctx context.Context, input NodeInput) (StorageBinding, error) {
				upload, err := outputs.Bind(ctx, input)
				return StorageBinding{Authority: upload.Authority, Bytes: bytesBackend, Records: outputs.Records}, err
			},
			PublishOutput: outputs.PublishOutput, PrepareArtifacts: artifacts.Prepare,
			Start: func(_ context.Context, input NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
				capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:memo-job")
				if err != nil || len(capacity.Holders) != 1 || capacity.UsedCost != 1 {
					t.Fatalf("memo entered before capacity: %+v error=%v", capacity, err)
				}
				started = append(started, input)
				return &executorProcess{plan: plan, result: nodeproto.RunResult{Outcome: "success", Output: json.RawMessage(`"memo-output"`)}, onRun: func(context.Context) { bodies++ }, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpEval {
						return nodeproto.EvalResult{Value: json.RawMessage(`"same-key"`)}, nil
					}
					return nil, nil
				}}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		admission := ComposeStartGate(f.Tenant, &LocalAdmission{Daemon: daemon}, false)
		if err := admission.claimedNode(ctx, input, func(ctx context.Context) error { return host.Execute(ctx, input) }); err != nil {
			t.Fatal(err)
		}
		row, err := f.Tenant.ReadNode(ctx, ref)
		want := store.NodeSuccess
		if run == 1 {
			want = store.NodeMemoized
		}
		if err != nil || row.Status != store.StatusDone || row.Outcome != want || row.Attempt != 1 || row.ClaimedBy != "" || row.OutputRecord == nil || row.ManifestRecord == nil || bodies != 1 {
			t.Fatalf("run=%d row=%+v bodies=%d error=%v", run, row, bodies, err)
		}
		attempt := runspec.AttemptIdentity{Team: request.Team, RunID: f.Input.RunID, NodeID: job.ID, Attempt: int64(row.Attempt), Generation: int64(row.Generation)}
		for _, record := range []*blob.RecordKey{row.OutputRecord, row.ManifestRecord} {
			if *record != blob.ResultRecordKey(attempt, request.Git.Repository.Key, blob.ScopeFor(spec.Request.Provenance), request.Pipeline, record.Kind) {
				t.Fatalf("memo attached another attempt's record: %+v", record)
			}
		}
		if live, granted, released := daemon.counts(); live != 0 || granted != 1 || released != 1 {
			t.Fatalf("run=%d admission live/granted/released=%d/%d/%d", run, live, granted, released)
		}
		capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:memo-job")
		if err != nil || len(capacity.Holders) != 0 || len(capacity.Waiters) != 0 || capacity.UsedCost != 0 {
			t.Fatalf("memo retained job capacity: %+v error=%v", capacity, err)
		}
		for _, process := range started {
			if process.Attempt != 1 || process.Storage.Authority.Attempt.Value.Attempt != 1 || process.GrantedRunner.Name != input.GrantedRunner.Name {
				t.Fatal("memo started without the actual job award")
			}
		}
	}
}
