package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestBlobRecordsLookupCannotBorrowAnotherRecordsDigestAuthority(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	other := addBlobJob(t, s, intent, "unrelated")
	if _, err := commitTestBlob(t, records.tenant, other); err != nil {
		t.Fatal(err)
	}
	if _, err := records.CommitRecord(t.Context(), blobOutputRequest(other)); err != nil {
		t.Fatal(err)
	}
	if _, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: blobOutputRequest(other).Key}); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("foreign record metadata borrowed readable digest: %v", err)
	}
}

func TestBlobRecordsMemoRequiresDeclaredMemoization(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	request.Key.Kind, request.Key.Name, request.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'", []byte(`{"id":"job"}`))
	if _, err := records.CommitRecord(t.Context(), request); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("undeclared memo write: %v", err)
	}
	lookup := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: request.Key.Scope.Repo, Ref: request.Key.Scope.Ref, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
	if _, err := records.LookupMemo(t.Context(), lookup); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("undeclared memo lookup: %v", err)
	}
}

func TestBlobRecordsOwnBinaryLookup(t *testing.T) {
	_, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	request.Key.Kind = blob.KindBinary
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	got, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: request.Key})
	if err != nil || !got.Found || got.Record.Digest != intent.Digest {
		t.Fatalf("own binary=%+v: %v", got, err)
	}
}

func TestBlobRecordsMemoRefusesBareKey(t *testing.T) {
	_, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	request.Key.Kind, request.Key.Name, request.Key.Scope.Key = blob.KindMemo, "output", ""
	if _, err := records.CommitRecord(t.Context(), request); err == nil {
		t.Fatal("bare memo key accepted")
	}
}
