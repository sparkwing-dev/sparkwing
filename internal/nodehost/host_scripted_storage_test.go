package nodehost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type scriptedStorage struct {
	onPut            func(context.Context, blob.PutRequest) (blob.Receipt, error)
	onGet            func(context.Context, blob.GetRequest) (blob.VerifiedReader, error)
	onHasMany        func(context.Context, blob.HasManyRequest) (blob.Presence, error)
	onLookupRecord   func(context.Context, blob.RecordLookup) (blob.RecordLookupResult, error)
	onLookupMemo     func(context.Context, blob.MemoLookup) (blob.MemoHit, error)
	onLookupDepcache func(context.Context, blob.DepcacheLookup) (blob.DepcacheHit, error)
	onCommitRecord   func(context.Context, blob.CommitRecordRequest) (blob.Record, error)
	onCopyMemo       func(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error)
}

func (s *scriptedStorage) Put(ctx context.Context, q blob.PutRequest) (blob.Receipt, error) {
	if s.onPut == nil {
		return blob.Receipt{}, errors.New("unexpected blob put")
	}
	return s.onPut(ctx, q)
}
func (s *scriptedStorage) Get(ctx context.Context, q blob.GetRequest) (blob.VerifiedReader, error) {
	if s.onGet == nil {
		return nil, errors.New("unexpected blob get")
	}
	return s.onGet(ctx, q)
}
func (s *scriptedStorage) HasMany(ctx context.Context, q blob.HasManyRequest) (blob.Presence, error) {
	if s.onHasMany == nil {
		return blob.Presence{}, errors.New("unexpected blob presence")
	}
	return s.onHasMany(ctx, q)
}
func (s *scriptedStorage) LookupRecord(ctx context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
	if s.onLookupRecord == nil {
		return blob.RecordLookupResult{}, errors.New("unexpected record lookup")
	}
	return s.onLookupRecord(ctx, q)
}
func (s *scriptedStorage) LookupMemo(ctx context.Context, q blob.MemoLookup) (blob.MemoHit, error) {
	if s.onLookupMemo == nil {
		return blob.MemoHit{}, errors.New("unexpected memo lookup")
	}
	return s.onLookupMemo(ctx, q)
}
func (s *scriptedStorage) LookupDepcache(ctx context.Context, q blob.DepcacheLookup) (blob.DepcacheHit, error) {
	if s.onLookupDepcache == nil {
		return blob.DepcacheHit{}, errors.New("unexpected depcache lookup")
	}
	return s.onLookupDepcache(ctx, q)
}
func (s *scriptedStorage) CommitRecord(ctx context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	if s.onCommitRecord == nil {
		return blob.Record{}, errors.New("unexpected record commit")
	}
	return s.onCommitRecord(ctx, q)
}
func (s *scriptedStorage) CopyMemo(ctx context.Context, q blob.CopyMemoRequest) (blob.MemoHit, error) {
	if s.onCopyMemo == nil {
		return blob.MemoHit{}, errors.New("unexpected memo copy")
	}
	return s.onCopyMemo(ctx, q)
}

type scriptedRemoteCache struct {
	onMemo     func(context.Context, blob.MemoKey, blob.Budget) (blob.MemoHit, error)
	onDepcache func(context.Context, blob.DepcacheKey, blob.Budget) (blob.Record, error)
	onArtifact func(context.Context, blob.Digest) (blob.VerifiedReader, error)
}

func (s *scriptedRemoteCache) Memo(ctx context.Context, key blob.MemoKey, b blob.Budget) (blob.MemoHit, error) {
	if s.onMemo == nil {
		return blob.MemoHit{}, errors.New("unexpected remote memo")
	}
	return s.onMemo(ctx, key, b)
}
func (s *scriptedRemoteCache) Depcache(ctx context.Context, key blob.DepcacheKey, b blob.Budget) (blob.Record, error) {
	if s.onDepcache == nil {
		return blob.Record{}, errors.New("unexpected remote depcache")
	}
	return s.onDepcache(ctx, key, b)
}
func (s *scriptedRemoteCache) Artifact(ctx context.Context, d blob.Digest) (blob.VerifiedReader, error) {
	if s.onArtifact == nil {
		return nil, errors.New("unexpected remote artifact")
	}
	return s.onArtifact(ctx, d)
}

type artifactPublisherFixture struct {
	*scriptedStorage
	upload    JobUpload
	mode      string
	puts      []blob.PutRequest
	values    map[blob.Digest][]byte
	transfers [][]byte
	commit    *blob.CommitRecordRequest
	beforePut func()
}
type artifactBackendFixture struct {
	*scriptedStorage
	lookup          blob.RecordLookupResult
	request         blob.RecordLookup
	manifest, value []byte
	mode            string
	cancel          context.CancelCauseFunc
	gets            []blob.GetRequest
	readers         []*outputReader
}
type depcacheBackendFixture struct {
	*artifactPublisherFixture
	lookup  *blob.DepcacheLookup
	reader  *outputReader
	lookups int
}
type remoteDirectoryCacheFixture struct {
	*scriptedRemoteCache
	backend *depcacheBackendFixture
	record  blob.Record
	key     blob.DepcacheKey
	reads   int
	lookups int
}
type memoBackendFixture struct {
	*scriptedStorage
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
type remoteMemoCacheFixture struct {
	*scriptedRemoteCache
	backend *memoBackendFixture
	key     blob.MemoKey
	budget  blob.Budget
	lookups int
}
type outputBackendFixture struct {
	*scriptedStorage
	mode   string
	upload JobUpload
	value  []byte
	put    *blob.PutRequest
	commit *blob.CommitRecordRequest
	lookup *blob.RecordLookup
	get    *blob.GetRequest
	reader *outputReader
}

func newArtifactBackend(manifest, value []byte) *artifactBackendFixture {
	b := &artifactBackendFixture{scriptedStorage: &scriptedStorage{}, manifest: manifest, value: value}
	b.onLookupRecord = func(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
		b.request = request
		if b.mode == "lookup" {
			return blob.RecordLookupResult{}, outputRefused
		}
		return b.lookup, nil
	}
	b.onGet = func(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
		b.gets = append(b.gets, request)
		if request.Authority != b.request.Authority {
			return nil, blob.ErrDigestNotReadable
		}
		var value []byte
		var prefix string
		switch request.Digest {
		case outputDigest(b.manifest):
			value, prefix = b.manifest, "manifest_"
		case outputDigest(b.value):
			value, prefix = b.value, "member_"
		default:
			return nil, blob.ErrNotFound
		}
		mode, _ := strings.CutPrefix(b.mode, prefix)
		if mode == "missing" {
			return nil, blob.ErrNotFound
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
		if mode == "cancel" {
			b.cancel(outputRefused)
		}
		return reader, nil
	}
	return b
}
func newOutputBackend(mode string, upload JobUpload, value []byte) *outputBackendFixture {
	b := &outputBackendFixture{scriptedStorage: &scriptedStorage{}, mode: mode, upload: upload, value: value}
	b.onPut = func(_ context.Context, request blob.PutRequest) (blob.Receipt, error) {
		b.put = &request
		value, err := io.ReadAll(request.Body)
		if err != nil {
			return blob.Receipt{}, err
		}
		if !bytes.Equal(value, b.value) || request.Digest != outputDigest(value) || request.Size != int64(len(value)) || request.Identity != b.upload.Identity || request.Authority != b.upload.Authority {
			return blob.Receipt{}, errors.New("upload changed bytes or authority")
		}
		if b.mode == "put" {
			return blob.Receipt{}, outputRefused
		}
		receipt := blob.Receipt{Identity: request.Identity, Digest: request.Digest}
		if b.mode == "receipt_digest" {
			receipt.Digest = outputDigest([]byte("other"))
		}
		if b.mode == "receipt_attempt" {
			receipt.Identity.Attempt.Value.Attempt++
		}
		return receipt, nil
	}
	b.onCommitRecord = func(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
		b.commit = &request
		if b.mode == "commit" {
			return blob.Record{}, outputRefused
		}
		record := blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source}
		if b.mode == "record_key" {
			record.Key.Scope.Job = "foreign"
		}
		if b.mode == "record_digest" {
			record.Digest = outputDigest([]byte("other"))
		}
		if b.mode == "record_attempt" {
			record.Source.Attempt.Value.Generation++
		}
		return record, nil
	}
	b.onLookupRecord = func(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
		b.lookup = &request
		if b.mode == "lookup" {
			return blob.RecordLookupResult{}, outputRefused
		}
		if b.mode == "missing_record" {
			return blob.RecordLookupResult{}, nil
		}
		record := blob.Record{Key: request.Key, Digest: outputDigest(b.value)}
		if b.mode == "lookup_key" {
			record.Key.Scope.Job = "foreign"
		}
		return blob.RecordLookupResult{Found: true, Record: record}, nil
	}
	b.onGet = func(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
		b.get = &request
		if b.mode == "missing_bytes" {
			return nil, blob.ErrNotFound
		}
		if b.mode == "forbidden" {
			return nil, blob.ErrDigestNotReadable
		}
		b.reader = &outputReader{Reader: bytes.NewReader(b.value), mode: b.mode}
		return b.reader, nil
	}
	return b
}
func newArtifactPublisher(upload JobUpload) *artifactPublisherFixture {
	p := &artifactPublisherFixture{scriptedStorage: &scriptedStorage{}, upload: upload, values: map[blob.Digest][]byte{}}
	p.onPut = func(_ context.Context, request blob.PutRequest) (blob.Receipt, error) {
		p.puts = append(p.puts, request)
		if p.beforePut != nil {
			p.beforePut()
		}
		value, err := io.ReadAll(request.Body)
		if err != nil {
			return blob.Receipt{}, err
		}
		p.transfers = append(p.transfers, value)
		if request.Digest != outputDigest(value) || request.Size != int64(len(value)) {
			return blob.Receipt{}, blob.ErrIntegrity
		}
		if request.Identity != p.upload.Identity || request.Authority != p.upload.Authority {
			return blob.Receipt{}, blob.ErrDigestNotReadable
		}
		if p.mode == "put" || p.mode == "manifest_put" && len(p.puts) > 1 {
			return blob.Receipt{}, outputRefused
		}
		p.values[request.Digest] = value
		receipt := blob.Receipt{Identity: request.Identity, Digest: request.Digest}
		if p.mode == "receipt" {
			receipt.Identity.Attempt.Value.Generation++
		}
		return receipt, nil
	}
	p.onCommitRecord = func(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
		p.commit = &request
		if p.mode == "commit" {
			return blob.Record{}, outputRefused
		}
		if request.Key != p.upload.Key || request.Source != p.upload.Identity || request.Authority != p.upload.Authority {
			return blob.Record{}, blob.ErrDigestNotReadable
		}
		if _, ok := p.values[request.Digest]; !ok {
			return blob.Record{}, blob.ErrNotFound
		}
		for _, member := range request.Members {
			value, ok := p.values[member.Digest]
			if !ok || member.ManifestDigest != request.Digest || member.Team != request.Key.Team || int64(len(value)) != member.Size {
				return blob.Record{}, blob.ErrIntegrity
			}
		}
		record := blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source}
		if p.mode == "record" {
			record.Source.Attempt.Value.Attempt++
		}
		return record, nil
	}
	p.onLookupRecord = func(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
		if p.commit == nil || request.Key != p.commit.Key {
			return blob.RecordLookupResult{}, nil
		}
		if request.Authority != p.upload.Authority {
			return blob.RecordLookupResult{}, blob.ErrDigestNotReadable
		}
		return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: p.commit.Key, Digest: p.commit.Digest, Source: p.commit.Source}, Members: p.commit.Members}, nil
	}
	p.onGet = func(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
		if request.Authority != p.upload.Authority {
			return nil, blob.ErrDigestNotReadable
		}
		value, ok := p.values[request.Digest]
		if !ok {
			return nil, blob.ErrNotFound
		}
		return &outputReader{Reader: bytes.NewReader(value)}, nil
	}
	return p
}
func newMemoBackend(hit blob.MemoHit, values map[blob.Digest][]byte) *memoBackendFixture {
	b := &memoBackendFixture{scriptedStorage: &scriptedStorage{}, hit: hit, values: values}
	b.onLookupMemo = func(_ context.Context, request blob.MemoLookup) (blob.MemoHit, error) {
		b.lookup = &request
		if b.mode == "lookup" {
			return blob.MemoHit{}, outputRefused
		}
		return b.hit, nil
	}
	b.onCopyMemo = func(_ context.Context, request blob.CopyMemoRequest) (blob.MemoHit, error) {
		b.copy = &request
		if b.mode == "commit" {
			return blob.MemoHit{}, outputRefused
		}
		return b.hit, nil
	}
	b.onHasMany = func(ctx context.Context, request blob.HasManyRequest) (blob.Presence, error) {
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
	b.onGet = func(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
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
	return b
}
func newDepcacheBackend(publisher *artifactPublisherFixture) *depcacheBackendFixture {
	b := &depcacheBackendFixture{artifactPublisherFixture: publisher}
	b.onLookupDepcache = func(_ context.Context, request blob.DepcacheLookup) (blob.DepcacheHit, error) {
		b.lookup = &request
		b.lookups++
		if b.mode == "lookup" {
			return blob.DepcacheHit{}, outputRefused
		}
		if b.commit == nil || b.commit.Key.Scope.Key != request.Key.Key {
			return blob.DepcacheHit{}, nil
		}
		if request.Authority != b.upload.Authority || request.Key.Repo != b.upload.Key.Scope.Repo || request.Key.Ref != b.upload.Key.Scope.Ref || request.Key.Name != b.upload.Key.Name {
			return blob.DepcacheHit{}, blob.ErrDigestNotReadable
		}
		record := blob.Record{Key: b.commit.Key, Digest: b.commit.Digest, Source: b.commit.Source}
		switch b.mode {
		case "wrong_team":
			record.Key.Team = "foreign"
		case "wrong_repo":
			record.Key.Scope.Repo = "example.com/foreign/repo"
		case "wrong_key":
			record.Key.Scope.Key = "foreign-key"
		case "wrong_name":
			record.Key.Name = "foreign-cache"
		case "wrong_kind":
			record.Key.Kind = blob.KindOutput
		}
		return blob.DepcacheHit{Found: true, Source: blob.RestoreOwnRef, Record: record}, nil
	}
	b.onGet = func(ctx context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		if b.mode == "missing_bytes" {
			return nil, blob.ErrNotFound
		}
		if b.mode == "forbidden" || request.Authority != b.upload.Authority {
			return nil, blob.ErrDigestNotReadable
		}
		value, ok := b.values[request.Digest]
		if !ok {
			return nil, blob.ErrNotFound
		}
		b.reader = &outputReader{Reader: bytes.NewReader(value), mode: b.mode}
		return b.reader, nil
	}
	return b
}
func newRemoteMemoCache(backend *memoBackendFixture) *remoteMemoCacheFixture {
	r := &remoteMemoCacheFixture{scriptedRemoteCache: &scriptedRemoteCache{}, backend: backend}
	r.onMemo = func(_ context.Context, key blob.MemoKey, budget blob.Budget) (blob.MemoHit, error) {
		r.lookups++
		r.key, r.budget = key, budget
		return r.backend.hit, nil
	}
	r.onArtifact = func(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		return r.backend.Get(ctx, blob.GetRequest{Authority: blob.Authority{CapabilityRef: "remote-only"}, Digest: digest})
	}
	return r
}
func newRemoteDirectoryCache(backend *depcacheBackendFixture, record blob.Record) *remoteDirectoryCacheFixture {
	r := &remoteDirectoryCacheFixture{scriptedRemoteCache: &scriptedRemoteCache{}, backend: backend, record: record}
	r.onDepcache = func(_ context.Context, key blob.DepcacheKey, _ blob.Budget) (blob.Record, error) {
		r.lookups++
		r.key = key
		return r.record, nil
	}
	r.onArtifact = func(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		r.reads++
		return r.backend.Get(ctx, blob.GetRequest{Authority: r.backend.upload.Authority, Digest: digest})
	}
	return r
}
