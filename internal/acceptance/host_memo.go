package acceptance

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type memoBackend struct {
	blob.Store
	blob.RecordStore
	hit      blob.MemoHit
	values   map[blob.Digest][]byte
	mode     string
	broken   blob.Digest
	lookup   *blob.MemoLookup
	copy     *blob.CopyMemoRequest
	presence *blob.HasManyRequest
	gets     []blob.GetRequest
	readers  []*outputReader
	cancel   context.CancelCauseFunc
}

func (b *memoBackend) LookupMemo(_ context.Context, request blob.MemoLookup) (blob.MemoHit, error) {
	b.lookup = &request
	if b.mode == "lookup" {
		return blob.MemoHit{}, outputRefused
	}
	return b.hit, nil
}

func (b *memoBackend) CopyMemo(_ context.Context, request blob.CopyMemoRequest) (blob.MemoHit, error) {
	b.copy = &request
	if b.mode == "commit" {
		return blob.MemoHit{}, outputRefused
	}
	return b.hit, nil
}

func (b *memoBackend) HasMany(ctx context.Context, request blob.HasManyRequest) (blob.Presence, error) {
	b.presence = &request
	if b.mode == "presence" || b.mode == "budget" {
		return blob.Presence{}, blob.ErrBudget
	}
	if b.mode == "cancel" {
		b.cancel(outputRefused)
		return blob.Presence{}, context.Cause(ctx)
	}
	var result blob.Presence
	for _, digest := range request.Digests {
		if _, ok := b.values[digest]; ok && !(digest == b.broken && b.mode == "missing") {
			result.Present = append(result.Present, digest)
		}
	}
	return result, nil
}

func (b *memoBackend) Get(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
	b.gets = append(b.gets, request)
	value, ok := b.values[request.Digest]
	if !ok || request.Digest == b.broken && b.mode == "missing" {
		return nil, blob.ErrNotFound
	}
	mode := ""
	if request.Digest == b.broken {
		mode = b.mode
	}
	if mode == "denied" {
		return nil, blob.ErrDigestNotReadable
	}
	if mode == "nil_reader" {
		return nil, nil
	}
	reader := &outputReader{Reader: bytes.NewReader(value), mode: mode}
	b.readers = append(b.readers, reader)
	if mode == "reader_error" {
		return reader, outputRefused
	}
	return reader, nil
}

func memoFixture() (nodehost.StorageBinding, blob.MemoKey, nodeproto.JobSpec, *memoBackend) {
	output, manifest, file := []byte(`{"built":"expected"}`), []byte(`{"files":[]}`), []byte("compiled executable")
	attempt := runspec.AttemptIdentity{Team: "team", RunID: "destination", NodeID: "build", Generation: 2, Attempt: 3}
	authority := blob.Authority{CapabilityRef: "live-reader", Principal: "reader", Repo: "example.com/acme/repo", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "feature"}}, Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
	hit := blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: outputDigest(output)}, Manifest: runspec.Optional[blob.Digest]{Set: true, Value: outputDigest(manifest)}, Members: []blob.Member{{Team: "team", ManifestDigest: outputDigest(manifest), Digest: outputDigest(file), Path: "bin/tool", Size: int64(len(file)), Mode: 0o755}}}
	hit.ReadSet.Reader = attempt
	backend := &memoBackend{hit: hit, values: map[blob.Digest][]byte{hit.Output.Value: output, hit.Manifest.Value: manifest, outputDigest(file): file}}
	key := blob.MemoKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: "pipeline", Job: "build", Hash: "author-key"}
	job := nodeproto.JobSpec{ID: key.Job, Memo: &nodeproto.MemoSpec{}, ResultStep: "body", Artifacts: []string{"bin/tool"}}
	return nodehost.StorageBinding{Bytes: backend, Records: backend, Authority: authority}, key, job, backend
}

// CheckMemoRecords checks verified reuse and miss behavior through the declared storage interfaces.
func CheckMemoRecords(t *testing.T) {
	t.Helper()
	for _, copyHit := range []bool{false, true} {
		name := "lookup"
		if copyHit {
			name = "copy"
		}
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"valid", "lookup", "commit", "absent", "no_output", "no_manifest", "presence", "budget", "missing", "denied", "corrupt", "unverified", "close", "nil_reader", "reader_error", "invalid_json", "wrong_size", "invalid_digest", "wrong_manifest", "wrong_reader", "duplicate_path", "parent_path", "orphan_member"} {
				t.Run(mode, func(t *testing.T) {
					storage, key, job, backend := memoFixture()
					backend.mode, backend.broken = mode, backend.hit.Output.Value
					switch mode {
					case "absent":
						backend.hit.Found = false
					case "no_output":
						backend.hit.Output = runspec.Optional[blob.Digest]{}
					case "no_manifest":
						backend.hit.Manifest = runspec.Optional[blob.Digest]{}
					case "invalid_json":
						backend.values[backend.broken] = []byte("not JSON")
					case "wrong_size":
						backend.hit.Members[0].Size++
					case "invalid_digest":
						backend.hit.Output.Value = "bad"
					case "wrong_manifest":
						backend.hit.Members[0].ManifestDigest = outputDigest([]byte("foreign"))
					case "wrong_reader":
						backend.hit.ReadSet.Reader.Generation++
					case "duplicate_path":
						backend.hit.Members = append(backend.hit.Members, backend.hit.Members[0])
					case "parent_path":
						backend.hit.Members[0].Path = "../foreign"
					case "orphan_member":
						job.Artifacts, backend.hit.Manifest = nil, runspec.Optional[blob.Digest]{}
					}
					budget := blob.Budget{Deadline: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
					adapter := nodehost.MemoRecords{Budget: budget, Warn: func(string) {}}
					var got blob.MemoHit
					var err error
					if copyHit {
						got, err = adapter.Copy(t.Context(), storage, blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: storage.Authority, Key: key, Budget: budget}, Destination: storage.Authority.Attempt.Value}, job)
					} else {
						got, err = adapter.Lookup(t.Context(), storage, key, job)
					}
					want := mode == "valid" || copyHit && mode == "lookup" || !copyHit && mode == "commit"
					if err != nil || got.Found != want || !want && !reflect.DeepEqual(got, blob.MemoHit{}) {
						t.Fatalf("memo=%+v, %v; want hit=%t", got, err, want)
					}
					if want && (len(backend.gets) != 3 || backend.presence == nil || len(backend.presence.Digests) != 3 || backend.presence.Budget != budget) {
						t.Fatalf("verification did not cover all bytes: gets=%v presence=%+v", backend.gets, backend.presence)
					}
					for _, get := range backend.gets {
						if get.Authority != storage.Authority {
							t.Fatal("memo widened read authority")
						}
					}
					for _, reader := range backend.readers {
						if !reader.closed || want && (!reader.eof || !reader.Verified()) {
							t.Fatal("memo leaked or accepted an unverified reader")
						}
					}
				})
			}
		})
	}
	for _, part := range []string{"output", "manifest", "member"} {
		t.Run("missing_"+part, func(t *testing.T) {
			storage, key, job, backend := memoFixture()
			backend.broken = backend.hit.Output.Value
			if part == "manifest" {
				backend.broken = backend.hit.Manifest.Value
			} else if part == "member" {
				backend.broken = backend.hit.Members[0].Digest
			}
			backend.mode = "missing"
			got, err := (nodehost.MemoRecords{Warn: func(string) {}}).Lookup(t.Context(), storage, key, job)
			if err != nil || got.Found || len(backend.gets) != 0 || backend.presence == nil {
				t.Fatalf("authoritative existence failure was not a miss: %+v, %v", got, err)
			}
		})
	}
	t.Run("cancel", func(t *testing.T) {
		storage, key, job, backend := memoFixture()
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		backend.cancel, backend.mode = cancel, "cancel"
		got, err := (nodehost.MemoRecords{Warn: func(string) {}}).Lookup(ctx, storage, key, job)
		if !errors.Is(err, outputRefused) || got.Found {
			t.Fatalf("lost cancellation: %+v, %v", got, err)
		}
	})
	for _, output := range [][]byte{[]byte("null"), nil} {
		t.Run("optional_output", func(t *testing.T) {
			storage, key, job, backend := memoFixture()
			job.ResultStep, job.Artifacts = "", nil
			backend.hit.Members, backend.hit.Manifest = nil, runspec.Optional[blob.Digest]{}
			backend.hit.Output = runspec.Optional[blob.Digest]{Set: output != nil, Value: outputDigest(output)}
			backend.values[backend.hit.Output.Value] = output
			got, err := (nodehost.MemoRecords{}).Lookup(t.Context(), storage, key, job)
			if err != nil || !got.Found {
				t.Fatalf("valid optional result became a miss: %+v, %v", got, err)
			}
		})
	}
	t.Run("expired_budget", func(t *testing.T) {
		storage, key, job, backend := memoFixture()
		adapter := nodehost.MemoRecords{Budget: blob.Budget{Deadline: time.Unix(1, 0)}, Warn: func(string) {}}
		got, err := adapter.Lookup(t.Context(), storage, key, job)
		if err != nil || got.Found || backend.lookup != nil {
			t.Fatalf("expired lookup budget must miss before IO: %+v, %v", got, err)
		}
		got, err = adapter.Copy(t.Context(), storage, blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: storage.Authority, Key: key, Budget: adapter.Budget}, Destination: storage.Authority.Attempt.Value}, job)
		if err != nil || got.Found || backend.copy != nil {
			t.Fatalf("expired copy budget must miss before IO: %+v, %v", got, err)
		}
	})
	t.Run("owner_lookup", func(t *testing.T) {
		storage, key, job, backend := memoFixture()
		storage.Authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
		storage.Authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "team", RunID: "destination"}}
		backend.hit.ReadSet.Reader = runspec.AttemptIdentity{}
		got, err := (nodehost.MemoRecords{}).Lookup(t.Context(), storage, key, job)
		if err != nil || !got.Found || backend.lookup == nil || backend.lookup.Authority.Attempt.Set || backend.copy != nil {
			t.Fatalf("owner lookup consumed or required a job attempt: %+v, %v", got, err)
		}
	})
	t.Run("empty_manifest", func(t *testing.T) {
		storage, key, job, backend := memoFixture()
		backend.hit.Members = nil
		got, err := (nodehost.MemoRecords{}).Lookup(t.Context(), storage, key, job)
		if err != nil || !got.Found || len(backend.gets) != 2 {
			t.Fatalf("empty manifest became a miss: %+v, %v", got, err)
		}
	})
}

type remoteMemoCache struct {
	blob.RemoteCache
	backend *memoBackend
	key     blob.MemoKey
	budget  blob.Budget
	lookups int
}

func (r *remoteMemoCache) Memo(_ context.Context, key blob.MemoKey, budget blob.Budget) (blob.MemoHit, error) {
	r.lookups++
	r.key, r.budget = key, budget
	return r.backend.hit, nil
}

func (r *remoteMemoCache) Artifact(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
	return r.backend.Get(ctx, blob.GetRequest{Authority: blob.Authority{CapabilityRef: "remote-only"}, Digest: digest})
}

// CheckRemoteMemo checks explicit read-only opt-in and separation from the local byte authority.
func CheckRemoteMemo(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"disabled", "enabled", "local_hit", "local_error", "remote_corrupt"} {
		t.Run(mode, func(t *testing.T) {
			storage, key, job, local := memoFixture()
			_, _, _, remote := memoFixture()
			remote.hit.Members[0].Team = "profile-team"
			profile := &remoteMemoCache{backend: remote}
			if mode != "disabled" {
				storage.RemoteCache = profile
			}
			local.hit.Found = mode == "local_hit"
			if mode == "local_error" {
				local.mode = "lookup"
			}
			if mode == "remote_corrupt" {
				remote.mode, remote.broken = "corrupt", remote.hit.Members[0].Digest
			}
			budget := blob.Budget{Deadline: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
			got, err := (nodehost.MemoRecords{Budget: budget, Warn: func(string) {}}).Lookup(t.Context(), storage, key, job)
			want := mode == "enabled" || mode == "local_hit"
			if err != nil || got.Found != want {
				t.Fatalf("remote lookup=%+v, %v", got, err)
			}
			if mode == "enabled" || mode == "remote_corrupt" {
				if profile.lookups != 1 || profile.key != key || profile.budget != budget || len(local.gets) != 0 || local.presence != nil || remote.presence != nil || local.copy != nil || remote.copy != nil {
					t.Fatal("remote read mixed authority, lost its budget or wrote records")
				}
			} else if profile.lookups != 0 || len(remote.gets) != 0 {
				t.Fatal("read remote bytes without an explicit local miss and opt-in")
			}
		})
	}
}

// CheckMemoVerification generates failed verification and proves that no usable hit survives it.
func CheckMemoVerification(t *testing.T, target, failure uint8, value string) {
	t.Helper()
	storage, key, job, backend := memoFixture()
	digests := []blob.Digest{backend.hit.Output.Value, backend.hit.Manifest.Value, backend.hit.Members[0].Digest}
	backend.broken = digests[int(target)%len(digests)]
	backend.mode = []string{"missing", "denied", "corrupt", "unverified", "close", "reader_error"}[int(failure)%6]
	backend.values[backend.broken] = []byte(value)
	got, err := (nodehost.MemoRecords{Warn: func(string) {}}).Lookup(t.Context(), storage, key, job)
	if err != nil || !reflect.DeepEqual(got, blob.MemoHit{}) {
		t.Fatalf("failed verification escaped as a result: %+v, %v", got, err)
	}
	for _, reader := range backend.readers {
		if !reader.closed {
			t.Fatal("failed memo read leaked its reader")
		}
	}
}

// CheckMemoScope generates mismatched requests and proves they never reach a backend.
func CheckMemoScope(t *testing.T, field uint8, value string) {
	t.Helper()
	storage, key, job, backend := memoFixture()
	request := blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: storage.Authority, Key: key}, Destination: storage.Authority.Attempt.Value}
	foreign := "foreign:" + value
	switch field % 12 {
	case 0:
		request.Lookup.Key.Repo = runspec.RepoKey(foreign)
	case 1:
		request.Lookup.Key.Ref.Ref = foreign
	case 2:
		request.Lookup.Key.Ref.Verified = !request.Lookup.Key.Ref.Verified
	case 3:
		request.Lookup.Key.Job = foreign
	case 4:
		request.Lookup.Authority.CapabilityRef = foreign
	case 5:
		request.Lookup.Authority.Principal = blob.Principal(foreign)
	case 6:
		request.Destination.Team = foreign
	case 7:
		request.Destination.RunID = foreign
	case 8:
		request.Destination.NodeID = foreign
	case 9:
		request.Destination.Generation++
	case 10:
		request.Destination.Attempt++
	case 11:
		job.Memo = nil
	}
	got, err := (nodehost.MemoRecords{}).Copy(t.Context(), storage, request, job)
	if err == nil || got.Found || backend.copy != nil || backend.presence != nil || len(backend.gets) != 0 {
		t.Fatalf("scope mismatch reached memo storage: %+v, %v", got, err)
	}
}
