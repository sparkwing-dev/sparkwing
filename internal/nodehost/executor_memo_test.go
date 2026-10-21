package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func executorMemoFixture(t *testing.T, value []byte) (*nodeExecutor, NodeInput, runspec.Spec, nodeproto.JobSpec, *executorSession, *memoBackendFixture) {
	t.Helper()
	input, _, deps, _ := executorFixture(t)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Team = "team"
	input.Accepted.Spec, _ = json.Marshal(spec)
	attempt := runspec.AttemptIdentity{Team: "team", RunID: input.Accepted.RunID, NodeID: input.NodeID, Attempt: input.Attempt, Generation: input.Generation}
	input.Storage.Authority = blob.Authority{CapabilityRef: "attempt-storage", Principal: "executor", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}, Repo: "repo", Ref: blob.RefFacts{Scope: blob.RefScope{Writer: "executor"}}}
	backend := newMemoBackend(blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: outputDigest(value)}}, map[blob.Digest][]byte{outputDigest(value): value})
	input.Storage.Bytes, input.Storage.Records = backend, backend
	process := &executorProcess{answer: func(_ context.Context, request nodeproto.Request) (any, error) {
		if request.Op != nodeproto.OpEval || request.Closure != "memo" {
			t.Fatal("unexpected memo request:", request)
		}
		return nodeproto.EvalResult{Value: json.RawMessage(`"key"`)}, nil
	}}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	return host.(*nodeExecutor), input, spec, nodeproto.JobSpec{ID: input.NodeID, Memo: &nodeproto.MemoSpec{}, Closures: nodeproto.ClosureSpec{MemoKey: "memo"}}, &executorSession{process: process}, backend
}

func TestExecutorMemoCopiesVerifiedHitToAwardedAttempt(t *testing.T) {
	host, input, spec, job, session, backend := executorMemoFixture(t, []byte(`{"value":3}`))
	hash, result, err := host.memo(t.Context(), session, input, job, spec)
	wantHash, _ := runspec.CanonicalHash("key")
	want := blob.ResultRecordKey(input.Storage.Authority.Attempt.Value, "repo", input.Storage.Authority.Ref.Scope, "ci", blob.KindOutput)
	if err != nil || hash != wantHash || result == nil || result.Outcome != store.NodeMemoized || result.OutputRecord == nil || *result.OutputRecord != want {
		t.Fatalf("hash=%s result=%+v error=%v", hash, result, err)
	}
	if backend.lookup == nil || backend.lookup.Authority != input.Storage.Authority || backend.lookup.Key.Hash != hash || backend.copy == nil || backend.copy.Destination != input.Storage.Authority.Attempt.Value {
		t.Fatal("memo lookup or copy escaped the awarded attempt")
	}
	for _, reader := range backend.readers {
		if !reader.closed || !reader.Verified() {
			t.Fatal("memo accepted an unverified or unclosed reader")
		}
	}
}

func TestExecutorMemoUnavailableDataReruns(t *testing.T) {
	for _, mode := range []string{"lookup", "missing", "corrupt", "unverified", "close", "denied", "nil_reader", "reader_error", "commit"} {
		t.Run(mode, func(t *testing.T) {
			host, input, spec, job, session, backend := executorMemoFixture(t, []byte(`{"value":3}`))
			backend.mode, backend.broken = mode, backend.hit.Output.Value
			hash, result, err := host.memo(t.Context(), session, input, job, spec)
			if err != nil || hash == "" || result != nil {
				t.Fatalf("cache failure prevented fresh execution: hash=%s result=%+v error=%v", hash, result, err)
			}
			if mode != "commit" && backend.copy != nil {
				t.Fatal("invalid bytes copied into result records")
			}
		})
	}
}

func TestExecutorMemoRechecksChangedCopy(t *testing.T) {
	host, input, spec, job, session, backend := executorMemoFixture(t, []byte(`{"value":3}`))
	backend.onCopyMemo = func(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error) {
		changed := backend.hit
		changed.Output.Value = outputDigest([]byte("missing"))
		return changed, nil
	}
	_, result, err := host.memo(t.Context(), session, input, job, spec)
	if err != nil || result != nil {
		t.Fatalf("changed copy reused unverified bytes: result=%+v error=%v", result, err)
	}
}

func TestExecutorMemoNoMemoStillEvaluatesKey(t *testing.T) {
	host, input, spec, job, session, backend := executorMemoFixture(t, nil)
	spec.Resolved.Settings.Options.NoMemo.Value = true
	hash, result, err := host.memo(t.Context(), session, input, job, spec)
	if err != nil || hash == "" || result != nil || backend.lookup != nil || backend.copy != nil || len(backend.gets) != 0 {
		t.Fatalf("NoMemo did not bypass reads: hash=%s result=%+v error=%v", hash, result, err)
	}
}

func TestExecutorMemoCancellationFailsInsteadOfMissing(t *testing.T) {
	host, input, spec, job, session, backend := executorMemoFixture(t, nil)
	ctx, cancel := context.WithCancelCause(t.Context())
	sentinel := errors.New("lease ended")
	backend.onLookupMemo = func(context.Context, blob.MemoLookup) (blob.MemoHit, error) {
		cancel(sentinel)
		return blob.MemoHit{}, sentinel
	}
	hash, result, err := host.memo(ctx, session, input, job, spec)
	if !errors.Is(err, sentinel) || hash != "" || result != nil {
		t.Fatalf("cancellation became cache miss: hash=%s result=%+v error=%v", hash, result, err)
	}
}

func TestExecutorMemoImportsRemoteArtifactsIntoLocalAttempt(t *testing.T) {
	host, input, spec, job, session, local := executorMemoFixture(t, []byte(`{"value":3}`))
	output := local.hit.Output
	file := []byte("artifact")
	manifest, _ := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{{Path: "file", Digest: outputDigest(file), Size: int64(len(file)), Mode: 0o644}}})
	manifestDigest := outputDigest(manifest)
	remote := newMemoBackend(blob.MemoHit{Found: true, Output: output, Manifest: runspec.Optional[blob.Digest]{Set: true, Value: manifestDigest}, Members: []blob.Member{{Team: "remote-team", ManifestDigest: manifestDigest, Digest: outputDigest(file), Path: "file", Size: int64(len(file)), Mode: 0o644}}}, map[blob.Digest][]byte{output.Value: local.values[output.Value], manifestDigest: manifest, outputDigest(file): file})
	input.Storage.RemoteCache = newRemoteMemoCache(remote)
	local.hit = blob.MemoHit{}
	var puts []blob.PutRequest
	var commits []blob.CommitRecordRequest
	local.onPut = func(_ context.Context, request blob.PutRequest) (blob.Receipt, error) {
		body, err := io.ReadAll(struct{ io.Reader }{request.Body})
		if err != nil || outputDigest(body) != request.Digest || int64(len(body)) != request.Size || request.Authority != input.Storage.Authority {
			t.Fatal("remote import changed bytes or authority")
		}
		puts = append(puts, request)
		return blob.Receipt{Identity: request.Identity, Digest: request.Digest}, nil
	}
	local.onCommitRecord = func(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
		commits = append(commits, request)
		return blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source}, nil
	}
	_, result, err := host.memo(t.Context(), session, input, job, spec)
	if err != nil || result == nil || result.OutputRecord == nil || result.ManifestRecord == nil || len(puts) != 3 || len(commits) != 2 {
		t.Fatalf("result=%+v puts=%d commits=%d error=%v", result, len(puts), len(commits), err)
	}
	if commits[1].Members[0].Team != "team" || remote.hit.Members[0].Team != "remote-team" || commits[1].Source.Attempt != input.Storage.Authority.Attempt {
		t.Fatal("remote manifest was not imported under local attempt authority")
	}
}

func TestExecutorMemoSavesExplicitEmptyOutputAndRetention(t *testing.T) {
	host, input, _, _, _, backend := executorMemoFixture(t, nil)
	input.Execution.MemoTTL.Value = time.Hour
	hash, _ := runspec.CanonicalHash("key")
	var commits []blob.CommitRecordRequest
	backend.onPut = func(_ context.Context, request blob.PutRequest) (blob.Receipt, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil || len(body) != 0 || request.Digest != outputDigest(nil) {
			t.Fatal("empty output was not stored explicitly")
		}
		return blob.Receipt{Identity: request.Identity, Digest: request.Digest}, nil
	}
	backend.onCommitRecord = func(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
		commits = append(commits, request)
		return blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source, ManifestDigest: request.ManifestDigest}, nil
	}
	before := time.Now()
	if err := host.saveMemo(t.Context(), input, hash, store.NodeResult{Outcome: store.NodeSuccess}); err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0].Key.Kind != blob.KindOutput || commits[1].Key.Kind != blob.KindMemo || commits[1].Digest != outputDigest(nil) || commits[1].Key.Scope.Key != string(hash) || !commits[1].ExpiresAt.Set || commits[1].ExpiresAt.Value.Before(before.Add(time.Hour)) {
		t.Fatalf("memo publication=%+v", commits)
	}
	for _, outcome := range []store.NodeState{store.NodeFailed, store.NodeSkipped, store.NodeMemoized} {
		if err := host.saveMemo(t.Context(), input, hash, store.NodeResult{Outcome: outcome}); err != nil || len(commits) != 2 {
			t.Fatalf("non-success published memo: outcome=%s error=%v", outcome, err)
		}
	}
}

func TestExecutorMemoVerifiesManifestAndEveryMember(t *testing.T) {
	host, input, spec, job, session, backend := executorMemoFixture(t, []byte(`null`))
	file := []byte("artifact")
	manifest, _ := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{{Path: "file", Digest: outputDigest(file), Size: int64(len(file)), Mode: 0o644}}})
	digest := outputDigest(manifest)
	backend.hit.Manifest = runspec.Optional[blob.Digest]{Set: true, Value: digest}
	backend.hit.Members = []blob.Member{{Team: "team", ManifestDigest: digest, Digest: outputDigest(file), Path: "file", Size: int64(len(file)), Mode: 0o644}}
	backend.values[digest], backend.values[outputDigest(file)] = manifest, file
	_, result, err := host.memo(t.Context(), session, input, job, spec)
	if err != nil || result == nil || result.ManifestRecord == nil || len(backend.gets) != 3 {
		t.Fatalf("manifest hit=%+v gets=%d error=%v", result, len(backend.gets), err)
	}
	backend.gets, backend.copy = nil, nil
	backend.values[outputDigest(file)] = bytes.Repeat([]byte("x"), len(file)+1)
	session = &executorSession{process: session.process}
	_, result, err = host.memo(t.Context(), session, input, job, spec)
	if err != nil || result != nil || backend.copy != nil {
		t.Fatalf("wrong-size member reused: result=%+v error=%v", result, err)
	}
}

func TestExecutorMemoPublishesOnlyCurrentResultReferences(t *testing.T) {
	host, input, _, _, _, backend := executorMemoFixture(t, []byte(`{"value":3}`))
	hash, _ := runspec.CanonicalHash("key")
	output := blob.ResultRecordKey(input.Storage.Authority.Attempt.Value, "repo", input.Storage.Authority.Ref.Scope, "ci", blob.KindOutput)
	manifest := blob.ResultRecordKey(input.Storage.Authority.Attempt.Value, "repo", input.Storage.Authority.Ref.Scope, "ci", blob.KindManifest)
	manifestDigest := outputDigest([]byte("manifest"))
	backend.onLookupRecord = func(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
		digest := backend.hit.Output.Value
		if request.Key == manifest {
			digest = manifestDigest
		}
		return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: request.Key, Digest: digest}}, nil
	}
	var committed *blob.CommitRecordRequest
	backend.onCommitRecord = func(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
		committed = &request
		return blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source, ManifestDigest: request.ManifestDigest}, nil
	}
	result := store.NodeResult{Outcome: store.NodeSuccess, OutputRecord: &output, ManifestRecord: &manifest}
	if err := host.saveMemo(t.Context(), input, hash, result); err != nil || committed == nil || committed.Digest != backend.hit.Output.Value || !committed.ManifestDigest.Set || committed.ManifestDigest.Value != manifestDigest {
		t.Fatalf("saved references=%+v error=%v", committed, err)
	}
	committed = nil
	output.Scope.Key = "another-attempt"
	if err := host.saveMemo(t.Context(), input, hash, result); !errors.Is(err, blob.ErrDigestNotReadable) || committed != nil {
		t.Fatalf("foreign result was published: %+v error=%v", committed, err)
	}
}
