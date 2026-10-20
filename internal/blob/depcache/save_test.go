package depcacheblob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type saveBoundary struct {
	blob.Store
	blob.RecordStore
	uploads, commits int
	receipt          func(*blob.Receipt)
	record           func(*blob.Record)
	afterUpload      func()
}

func (s *saveBoundary) Put(_ context.Context, q blob.PutRequest) (blob.Receipt, error) {
	s.uploads++
	if _, err := io.Copy(io.Discard, q.Body); err != nil {
		return blob.Receipt{}, err
	}
	r := blob.Receipt{Identity: q.Identity, Digest: q.Digest}
	if s.receipt != nil {
		s.receipt(&r)
	}
	if s.afterUpload != nil {
		s.afterUpload()
	}
	return r, nil
}

func (s *saveBoundary) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	s.commits++
	r := blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source}
	if s.record != nil {
		s.record(&r)
	}
	return r, nil
}

func saveData(s *saveBoundary) Data {
	i := blob.UploadIdentity{Kind: blob.UploadJob, Team: "team", RunID: "run", Principal: "principal", CapabilityRef: "cap", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 1, Generation: 2}}}
	a := blob.Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt, Repo: "gh:123", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "feature", Writer: "account/fixture"}}}
	return Data{Bytes: s, Records: s, Authority: a, Identity: i}
}

func TestDepcacheSaveRequiresMatchingReceiptAndRecord(t *testing.T) {
	for _, fault := range []string{"receipt_digest", "receipt_generation", "receipt_principal", "record_key", "record_digest", "record_source"} {
		t.Run(fault, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "deps"), []byte("archive bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			s := &saveBoundary{}
			switch fault {
			case "receipt_digest":
				s.receipt = func(r *blob.Receipt) { r.Digest = "other" }
			case "receipt_generation":
				s.receipt = func(r *blob.Receipt) { r.Identity.Attempt.Value.Generation++ }
			case "receipt_principal":
				s.receipt = func(r *blob.Receipt) { r.Identity.Principal = "other" }
			case "record_key":
				s.record = func(r *blob.Record) { r.Key.Name = "other" }
			case "record_digest":
				s.record = func(r *blob.Record) { r.Digest = "other" }
			case "record_source":
				s.record = func(r *blob.Record) { r.Source.Attempt.Value.Generation++ }
			}
			if n, err := New(saveData(s)).Store(t.Context(), "deps", source); n != 0 || !errors.Is(err, blob.ErrIntegrity) {
				t.Fatalf("invalid %s saved: %d, %v", fault, n, err)
			}
			wantCommits := 1
			if s.receipt != nil {
				wantCommits = 0
			}
			if s.uploads != 1 || s.commits != wantCommits {
				t.Fatalf("uploads/commits = %d/%d, want 1/%d", s.uploads, s.commits, wantCommits)
			}
		})
	}
}

func TestDepcacheSaveChecksAuthorityBeforeReadingFiles(t *testing.T) {
	for _, fault := range []string{"generation", "principal", "owner"} {
		t.Run(fault, func(t *testing.T) {
			s := &saveBoundary{}
			d := saveData(s)
			switch fault {
			case "generation":
				d.Identity.Attempt.Value.Generation++
			case "principal":
				d.Identity.Principal = "other"
			case "owner":
				d.Identity.Kind = blob.UploadOwnerUnit
				d.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
				d.Identity.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadPlanUnit, ID: "plan", Generation: 1}}
				d.Authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
				d.Authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: d.Identity}
			}
			missing := filepath.Join(t.TempDir(), "absent")
			if n, err := New(d).Store(t.Context(), "deps", missing); n != 0 || !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("invalid authority reached archive creation: %d, %v", n, err)
			}
			if s.uploads != 0 || s.commits != 0 {
				t.Fatal("invalid authority reached storage")
			}
		})
	}
}

func TestDepcacheSaveCancellationPreventsRecordCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &saveBoundary{afterUpload: cancel}
	if n, err := New(saveData(s)).Store(ctx, "deps", t.TempDir()); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload saved: %d, %v", n, err)
	}
	if s.uploads != 1 || s.commits != 0 {
		t.Fatalf("cancelled save uploads/commits = %d/%d", s.uploads, s.commits)
	}
}
