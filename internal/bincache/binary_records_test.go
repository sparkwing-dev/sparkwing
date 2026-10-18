package bincache

import (
	"bytes"
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
)

type binaryRecords struct {
	blob.RecordStore
	record  blob.Record
	err     error
	commits []blob.CommitRecordRequest
	lookups []blob.RecordLookup
}

func (r *binaryRecords) LookupRecord(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
	r.lookups = append(r.lookups, q)
	return blob.RecordLookupResult{Found: r.record.Digest != "", Record: r.record}, r.err
}

func (r *binaryRecords) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	r.commits = append(r.commits, q)
	return blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source, Provenance: blob.ProvenanceLocal}, r.err
}

type binaryBytes struct {
	blob.DataSigner
	body   []byte
	want   blob.Digest
	size   int64
	upload blob.UploadIntent
	gets   int
	putErr error
}

func (b *binaryBytes) Upload(_ context.Context, q blob.UploadIntent) (blob.UploadTicket, error) {
	b.upload = q
	return blob.UploadTicket{ID: "ticket", Transfer: blob.SignedTransfer{Digest: q.Digest, Size: q.Size}}, b.putErr
}

func (b *binaryBytes) Commit(context.Context, blob.CommitUpload) (blob.Receipt, error) {
	return blob.Receipt{Identity: b.upload.Identity, Digest: b.upload.Digest}, nil
}

func (b *binaryBytes) Download(_ context.Context, q blob.GetRequest) (blob.SignedTransfer, error) {
	b.gets++
	if q.Digest != b.want {
		return blob.SignedTransfer{}, blob.ErrNotFound
	}
	return blob.SignedTransfer{Digest: b.want, Size: b.size}, nil
}

func (b *binaryBytes) Transfer(ctx context.Context, _ blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if body != nil {
		var err error
		b.body, err = io.ReadAll(body)
		return io.NopCloser(bytes.NewReader(nil)), err
	}
	return io.NopCloser(bytes.NewReader(b.body)), nil
}

func binaryFixture() (*BinaryData, *binaryRecords, *binaryBytes) {
	i := blob.UploadIdentity{
		Kind: blob.UploadOwnerUnit, Team: "team", RunID: "run", Principal: "principal", CapabilityRef: "cap",
		Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadPlanUnit, ID: "plan", Generation: 3}},
	}
	a := blob.Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Repo: "gh:123", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "main"}}, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: i}}
	b := &binaryBytes{body: []byte("binary"), size: 6, want: blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("binary"))))}
	r := &binaryRecords{record: blob.Record{Key: blob.RecordKey{Team: "team", Kind: blob.KindBinary, Scope: blob.Scope{Repo: a.Repo, Ref: a.Ref.Scope}, Name: "build-key"}, Digest: b.want, Source: i, Provenance: blob.ProvenanceCloud}}
	return &BinaryData{
		Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: b, HTTPClient: b}), Records: r, Authority: a, Identity: i,
		Trust: blob.BuildTrust{Team: "team"}, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)},
	}, r, b
}

func TestBinaryRecordDownload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*BinaryData, *binaryRecords, *binaryBytes)
		want   error
	}{
		{"cloud", func(*BinaryData, *binaryRecords, *binaryBytes) {}, nil},
		{"local refused", func(_ *BinaryData, r *binaryRecords, _ *binaryBytes) { r.record.Provenance = blob.ProvenanceLocal }, blob.ErrDigestNotReadable},
		{"local opted in", func(d *BinaryData, r *binaryRecords, _ *binaryBytes) {
			r.record.Provenance = blob.ProvenanceLocal
			d.Trust.TrustLocalBuilds = true
		}, nil},
		{"foreign team", func(_ *BinaryData, r *binaryRecords, _ *binaryBytes) { r.record.Key.Team = "foreign" }, blob.ErrDigestNotReadable},
		{"foreign repo", func(_ *BinaryData, r *binaryRecords, _ *binaryBytes) { r.record.Key.Scope.Repo = "gh:456" }, blob.ErrDigestNotReadable},
		{"wrong build", func(_ *BinaryData, r *binaryRecords, _ *binaryBytes) { r.record.Key.Name = "other" }, blob.ErrDigestNotReadable},
		{"wrong ref", func(_ *BinaryData, r *binaryRecords, _ *binaryBytes) { r.record.Key.Scope.Ref.Verified = true }, blob.ErrDigestNotReadable},
		{"miss", func(_ *BinaryData, r *binaryRecords, _ *binaryBytes) { r.record = blob.Record{} }, ErrMiss},
		{"corrupt", func(_ *BinaryData, _ *binaryRecords, b *binaryBytes) { b.body = []byte("BINARY") }, blob.ErrIntegrity},
		{"short", func(_ *BinaryData, _ *binaryRecords, b *binaryBytes) { b.body = []byte("bin") }, blob.ErrIntegrity},
		{"long", func(_ *BinaryData, _ *binaryRecords, b *binaryBytes) { b.body = []byte("binary!") }, blob.ErrIntegrity},
		{"budget", func(d *BinaryData, _ *binaryRecords, _ *binaryBytes) { d.Budget.Deadline = time.Unix(1, 0) }, blob.ErrBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r, b := binaryFixture()
			tc.change(d, r, b)
			dir := t.TempDir()
			dest := filepath.Join(dir, "pipeline")
			if err := os.WriteFile(dest, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := DownloadBinary(t.Context(), d, "build-key", dest)
			if !errors.Is(err, tc.want) {
				t.Fatalf("download = %v, want %v", err, tc.want)
			}
			want := "previous"
			if tc.want == nil {
				want = "binary"
			}
			got, err := os.ReadFile(dest)
			if err != nil || string(got) != want {
				t.Fatalf("published %q, %v", got, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging files remain: %v, %v", entries, err)
			}
			if errors.Is(tc.want, blob.ErrDigestNotReadable) && b.gets != 0 {
				t.Fatal("read bytes before checking record authority")
			}
		})
	}
}

func TestBinaryRecordUpload(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			d, r, b := binaryFixture()
			if fail {
				b.putErr = blob.ErrIntegrity
			}
			path := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(path, []byte("upload"), 0o700); err != nil {
				t.Fatal(err)
			}
			err := UploadBinary(t.Context(), d, "build-key", path)
			if fail {
				if !errors.Is(err, blob.ErrIntegrity) || len(r.commits) != 0 {
					t.Fatalf("failed upload = %v, commits %v", err, r.commits)
				}
				return
			}
			if err != nil || len(r.commits) != 1 {
				t.Fatalf("upload = %v, commits %v", err, r.commits)
			}
			q := r.commits[0]
			if q.Key != r.record.Key || q.Source != d.Identity || q.Authority != d.Authority || q.Digest != blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("upload")))) || string(b.body) != "upload" {
				t.Fatalf("wrong binary commit: %+v", q)
			}
		})
	}
}

type binaryStreamStore struct {
	blob.Store
	reader blob.VerifiedReader
	err    error
}

func (s binaryStreamStore) Get(context.Context, blob.GetRequest) (blob.VerifiedReader, error) {
	return s.reader, s.err
}

type binaryStream struct {
	io.Reader
	verified bool
	closeErr error
	closes   int
}

func (r *binaryStream) Verified() bool { return r.verified }
func (r *binaryStream) Close() error   { r.closes++; return r.closeErr }

func TestBinaryDownloadRequiresVerificationAndClose(t *testing.T) {
	closeFailure := errors.New("close failed")
	for _, tc := range []struct {
		name            string
		verified        bool
		closeErr, error error
	}{
		{"unverified", false, nil, blob.ErrIntegrity},
		{"close failure", true, closeFailure, closeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _, _ := binaryFixture()
			stream := &binaryStream{Reader: bytes.NewBufferString("binary"), verified: tc.verified, closeErr: tc.closeErr}
			data.Bytes = binaryStreamStore{reader: stream}
			dir := t.TempDir()
			if err := DownloadBinary(t.Context(), data, "build-key", filepath.Join(dir, "binary")); !errors.Is(err, tc.error) {
				t.Fatalf("download = %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 || stream.closes != 1 {
				t.Fatalf("files %v, closes %d, error %v", entries, stream.closes, err)
			}
		})
	}
}

func TestBinaryDownloadCancelledBeforeLookup(t *testing.T) {
	data, records, _ := binaryFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := DownloadBinary(ctx, data, "build-key", filepath.Join(t.TempDir(), "binary"))
	if !errors.Is(err, context.Canceled) || len(records.lookups) != 0 {
		t.Fatalf("download %v, lookups %v", err, records.lookups)
	}
}

func TestBinaryDownloadPublicationFailureCleansStaging(t *testing.T) {
	data, _, _ := binaryFixture()
	dir := t.TempDir()
	dest := filepath.Join(dir, "directory")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := DownloadBinary(t.Context(), data, "build-key", dest); err == nil {
		t.Fatal("replaced a directory with an executable")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "directory" {
		t.Fatalf("staging remains: %v, %v", entries, err)
	}
}

type binaryReadFailure struct{}

func (binaryReadFailure) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestBinaryDownloadDeadlineFailuresAreBudgetFailures(t *testing.T) {
	for _, stage := range []string{"lookup", "download", "stream"} {
		t.Run(stage, func(t *testing.T) {
			data, records, _ := binaryFixture()
			switch stage {
			case "lookup":
				records.err = context.DeadlineExceeded
			case "download":
				data.Bytes = binaryStreamStore{err: context.DeadlineExceeded}
			case "stream":
				data.Bytes = binaryStreamStore{reader: &binaryStream{Reader: binaryReadFailure{}}}
			}
			dir := t.TempDir()
			err := DownloadBinary(t.Context(), data, "build-key", filepath.Join(dir, "binary"))
			if !errors.Is(err, blob.ErrBudget) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("download = %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("staging files remain: %v, %v", entries, err)
			}
		})
	}
}

type binaryRecordResponse struct {
	blob.RecordStore
	change func(*blob.Record)
}

func (r binaryRecordResponse) CommitRecord(ctx context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	record, err := r.RecordStore.CommitRecord(ctx, q)
	r.change(&record)
	return record, err
}

type binaryReceiptResponse struct {
	blob.Store
	receipt blob.Receipt
}

func (r binaryReceiptResponse) Put(context.Context, blob.PutRequest) (blob.Receipt, error) {
	return r.receipt, nil
}

func TestBinaryRecordUploadRefusesMismatchedResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*BinaryData)
		commits int
	}{
		{"receipt digest", func(d *BinaryData) {
			d.Bytes = binaryReceiptResponse{receipt: blob.Receipt{Identity: d.Identity, Digest: "other"}}
		}, 0},
		{"receipt owner", func(d *BinaryData) {
			sum := sha256.Sum256([]byte("bundle"))
			i := d.Identity
			i.RunID = "other"
			d.Bytes = binaryReceiptResponse{receipt: blob.Receipt{Identity: i, Digest: blob.Digest(fmt.Sprintf("%x", sum[:]))}}
		}, 0},
		{"record digest", func(d *BinaryData) {
			d.Records = binaryRecordResponse{d.Records, func(r *blob.Record) { r.Digest = "other" }}
		}, 1},
		{"record scope", func(d *BinaryData) {
			d.Records = binaryRecordResponse{d.Records, func(r *blob.Record) { r.Key.Scope.Repo = "gh:456" }}
		}, 1},
		{"record owner", func(d *BinaryData) {
			d.Records = binaryRecordResponse{d.Records, func(r *blob.Record) { r.Source.RunID = "other" }}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _, _ := binaryFixture()
			records := &binaryRecords{}
			data.Records = records
			tc.change(data)
			path := filepath.Join(t.TempDir(), "bundle")
			if err := os.WriteFile(path, []byte("bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := UploadBinary(t.Context(), data, "build-key", path)
			if !errors.Is(err, blob.ErrIntegrity) || len(records.commits) != tc.commits {
				t.Fatalf("upload = %v, commits = %d", err, len(records.commits))
			}
		})
	}
}

func TestBinaryRecordUploadRejectsAnotherOwner(t *testing.T) {
	data, _, transport := binaryFixture()
	data.Identity.Unit.Value.Generation++
	err := UploadBinary(t.Context(), data, "build-key", "must-not-be-opened")
	if !errors.Is(err, blob.ErrDigestNotReadable) || transport.upload.Identity.Kind != "" {
		t.Fatalf("upload = %v, byte request = %+v", err, transport.upload)
	}
}
