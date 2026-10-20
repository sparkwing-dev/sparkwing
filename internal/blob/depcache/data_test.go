package depcacheblob

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

type fixture struct {
	blob.RecordStore
	intent           blob.UploadIntent
	record           blob.Record
	data             []byte
	corrupt          bool
	lookups          int
	downloadCloseErr error
	onLookup         func()
}

func (f *fixture) Upload(_ context.Context, i blob.UploadIntent) (blob.UploadTicket, error) {
	f.intent = i
	return blob.UploadTicket{ID: "ticket", Transfer: blob.SignedTransfer{Digest: i.Digest, Size: i.Size}}, nil
}

func (f *fixture) Commit(context.Context, blob.CommitUpload) (blob.Receipt, error) {
	if d := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(f.data))); d != f.intent.Digest || int64(len(f.data)) != f.intent.Size {
		return blob.Receipt{}, blob.ErrIntegrity
	}
	return blob.Receipt{Identity: f.intent.Identity, Digest: f.intent.Digest}, nil
}

func (f *fixture) Download(_ context.Context, q blob.GetRequest) (blob.SignedTransfer, error) {
	if q.Digest != f.intent.Digest {
		return blob.SignedTransfer{}, blob.ErrNotFound
	}
	return blob.SignedTransfer{Digest: f.intent.Digest, Size: int64(len(f.data))}, nil
}

func (f *fixture) HasMany(context.Context, blob.HasManyRequest) (blob.Presence, error) {
	panic("unused")
}

func (f *fixture) Transfer(_ context.Context, _ blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	if body != nil {
		var err error
		f.data, err = io.ReadAll(body)
		return io.NopCloser(bytes.NewReader(nil)), err
	}
	data := bytes.Clone(f.data)
	if f.corrupt {
		data[len(data)-1] ^= 0xff
	}
	return archiveResponse{Reader: bytes.NewReader(data), closeErr: f.downloadCloseErr}, nil
}

type archiveResponse struct {
	io.Reader
	closeErr error
}

func (r archiveResponse) Close() error { return r.closeErr }
func (f *fixture) LookupDepcache(_ context.Context, q blob.DepcacheLookup) (blob.DepcacheHit, error) {
	f.lookups++
	if f.onLookup != nil {
		f.onLookup()
	}
	if q.Key.Repo != "gh:123" || q.Authority.Attempt.Value.NodeID != "job" {
		return blob.DepcacheHit{}, blob.ErrDigestNotReadable
	}
	return blob.DepcacheHit{Found: f.record.Digest != "", Record: f.record}, nil
}

func TestSignedDepcacheRejectsInvalidGzip(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tool"), []byte("new bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := tarsafe.WriteGzip(t.Context(), &archive, source); err != nil {
		t.Fatal(err)
	}
	raw := archive.Bytes()
	raw[len(raw)-8] ^= 1
	digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(raw)))
	f := &fixture{data: raw, intent: blob.UploadIntent{Digest: digest}, record: blob.Record{Digest: digest}}
	a := blob.Authority{Repo: "gh:123", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{NodeID: "job"}}}
	b := New(Data{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: f, HTTPClient: f}), Records: f, Authority: a, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}})
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "tool"), []byte("preserved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Fetch(t.Context(), "deps", dest); !errors.Is(err, gzip.ErrChecksum) {
		t.Fatalf("invalid gzip with valid blob digest = %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(dest, "tool")); err != nil || string(body) != "preserved" {
		t.Fatalf("failed restore changed destination: %q, %v", body, err)
	}
}

func TestDepcacheProbePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fixture{record: blob.Record{Digest: "present"}, onLookup: cancel}
	a := blob.Authority{Repo: "gh:123", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{NodeID: "job"}}}
	b := New(Data{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: f, HTTPClient: f}), Records: f, Authority: a, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}})
	if found, err := b.exists(ctx, "deps"); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe = %v, %v", found, err)
	}
}

func (f *fixture) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	f.record = blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source}
	return f.record, nil
}

func TestSignedDepcacheMissSaveHitAndCorruption(t *testing.T) {
	f := &fixture{}
	i := blob.UploadIdentity{Kind: blob.UploadJob, Team: "team", RunID: "run", Principal: "principal", CapabilityRef: "cap", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 1, Generation: 2}}}
	a := blob.Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt, Repo: "gh:123", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "feature", Writer: "account/fixture"}}}
	b := New(Data{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: f, HTTPClient: f}), Records: f, Authority: a, Identity: i, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}})
	if hit, err := b.exists(t.Context(), "deps"); err != nil || hit {
		t.Fatalf("initial lookup: %v %v", hit, err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tool"), []byte("executable bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	n, err := b.Store(t.Context(), "deps", source)
	if err != nil || n == 0 {
		t.Fatalf("store=%d %v", n, err)
	}
	if f.record.Key.Kind != blob.KindDepcache || f.record.Key.Scope.Ref.Verified || f.record.Source != i {
		t.Fatalf("incorrect record binding: %+v", f.record)
	}
	if hit, err := b.exists(t.Context(), "deps"); err != nil || !hit {
		t.Fatalf("saved lookup: %v %v", hit, err)
	}
	dest := filepath.Join(t.TempDir(), "restore")
	if got, err := b.Fetch(t.Context(), "deps", dest); err != nil || got != n {
		t.Fatalf("fetch=%d %v", got, err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "tool"))
	if err != nil || string(body) != "executable bytes" {
		t.Fatalf("restored=%q %v", body, err)
	}
	f.downloadCloseErr = errors.New("download close failed")
	if err := os.WriteFile(filepath.Join(dest, "tool"), []byte("preserved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Fetch(t.Context(), "deps", dest); !errors.Is(err, f.downloadCloseErr) {
		t.Fatalf("download close = %v", err)
	}
	body, err = os.ReadFile(filepath.Join(dest, "tool"))
	if err != nil || string(body) != "preserved" {
		t.Fatalf("failed close published archive: %q, %v", body, err)
	}
	f.downloadCloseErr = nil
	f.corrupt = true
	if _, err := b.Fetch(t.Context(), "deps", dest); err == nil {
		t.Fatal("corrupt archive published")
	}
	body, err = os.ReadFile(filepath.Join(dest, "tool"))
	if err != nil || string(body) != "preserved" {
		t.Fatal("failed restore changed destination")
	}
}
