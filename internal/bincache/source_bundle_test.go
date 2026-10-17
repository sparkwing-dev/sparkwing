package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func sourceBundleFixture(body []byte) (*SourceData, *binaryBytes) {
	binary, records, transport := binaryFixture()
	sum := sha256.Sum256(body)
	transport.body, transport.want, transport.size = body, blob.Digest(hex.EncodeToString(sum[:])), int64(len(body))
	return &SourceData{Bytes: binary.Bytes, Records: records, Authority: binary.Authority, Identity: binary.Identity, Budget: binary.Budget}, transport
}

func assertEmptyDirectory(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files remain: %v, %v", entries, err)
	}
}

func TestSourceBundleUploadCommitsExactReceipt(t *testing.T) {
	data, transport := sourceBundleFixture(nil)
	records := &binaryRecords{}
	data.Records = records
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(path, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	record, err := UploadSourceBundle(t.Context(), data, sha, path)
	if err != nil || len(records.commits) != 1 {
		t.Fatalf("upload = %v, commits = %v", err, records.commits)
	}
	q := records.commits[0]
	if q.Key.Kind != blob.KindSourceBundle || q.Key.Name != sha || q.Key.Team != data.Identity.Team || q.Key.Scope.Repo != data.Authority.Repo || q.Key.Scope.Ref != data.Authority.Ref.Scope || q.Source != data.Identity || q.Authority != data.Authority || record.Digest != q.Digest || string(transport.body) != "bundle" {
		t.Fatalf("wrong source record: %+v", q)
	}
	transport.putErr = blob.ErrIntegrity
	if _, err := UploadSourceBundle(t.Context(), data, sha, path); !errors.Is(err, blob.ErrIntegrity) || len(records.commits) != 1 {
		t.Fatalf("failed upload = %v, commits = %v", err, records.commits)
	}
}

type sourceRecordResponse struct {
	blob.RecordStore
	change func(*blob.Record)
}

func (r sourceRecordResponse) CommitRecord(ctx context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	record, err := r.RecordStore.CommitRecord(ctx, q)
	r.change(&record)
	return record, err
}

type sourceReceiptResponse struct {
	blob.Store
	receipt blob.Receipt
}

func (r sourceReceiptResponse) Put(context.Context, blob.PutRequest) (blob.Receipt, error) {
	return r.receipt, nil
}

func TestSourceBundleUploadRefusesMismatchedResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*SourceData)
		commits int
	}{
		{"receipt digest", func(d *SourceData) {
			d.Bytes = sourceReceiptResponse{receipt: blob.Receipt{Identity: d.Identity, Digest: "other"}}
		}, 0},
		{"receipt owner", func(d *SourceData) {
			sum := sha256.Sum256([]byte("bundle"))
			i := d.Identity
			i.RunID = "other"
			d.Bytes = sourceReceiptResponse{receipt: blob.Receipt{Identity: i, Digest: blob.Digest(hex.EncodeToString(sum[:]))}}
		}, 0},
		{"record digest", func(d *SourceData) {
			d.Records = sourceRecordResponse{d.Records, func(r *blob.Record) { r.Digest = "other" }}
		}, 1},
		{"record scope", func(d *SourceData) {
			d.Records = sourceRecordResponse{d.Records, func(r *blob.Record) { r.Key.Scope.Repo = "gh:456" }}
		}, 1},
		{"record owner", func(d *SourceData) {
			d.Records = sourceRecordResponse{d.Records, func(r *blob.Record) { r.Source.RunID = "other" }}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := sourceBundleFixture(nil)
			records := &binaryRecords{}
			data.Records = records
			tc.change(data)
			path := filepath.Join(t.TempDir(), "bundle")
			if err := os.WriteFile(path, []byte("bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := UploadSourceBundle(t.Context(), data, strings.Repeat("a", 40), path)
			if !errors.Is(err, blob.ErrIntegrity) || len(records.commits) != tc.commits {
				t.Fatalf("upload = %v, commits = %d", err, len(records.commits))
			}
		})
	}
}

func TestSourceBundleUploadRejectsAnotherOwner(t *testing.T) {
	data, transport := sourceBundleFixture(nil)
	data.Identity.Unit.Value.Generation++
	_, err := UploadSourceBundle(t.Context(), data, strings.Repeat("a", 40), "must-not-be-opened")
	if !errors.Is(err, blob.ErrDigestNotReadable) || transport.upload.Identity.Kind != "" {
		t.Fatalf("upload = %v, byte request = %+v", err, transport.upload)
	}
}
