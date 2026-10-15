package acceptance_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type storageMaterializationSigner struct {
	storageCacheSigner
	tickets map[string]blob.UploadIntent
}

func (s *storageMaterializationSigner) Upload(ctx context.Context, q blob.UploadIntent) (blob.UploadTicket, error) {
	id := fmt.Sprint(len(s.tickets) + 1)
	key := "teams/" + q.Identity.Team + "/pending/" + id
	object, err := s.objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: q.Size, SHA256: string(q.Digest), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		return blob.UploadTicket{}, err
	}
	s.tickets[id] = q
	return blob.UploadTicket{ID: id, Transfer: blob.SignedTransfer{URL: object.URL, Headers: object.Headers, Size: object.Size, Digest: q.Digest, ExpiresAt: object.ExpiresAt}}, nil
}

func (s *storageMaterializationSigner) Commit(ctx context.Context, q blob.CommitUpload) (blob.Receipt, error) {
	intent, ok := s.tickets[q.TicketID]
	if !ok || intent.Authority != q.Authority {
		return blob.Receipt{}, blob.ErrDigestNotReadable
	}
	pending := "teams/" + intent.Identity.Team + "/pending/" + q.TicketID
	if err := s.objects.CommitObject(ctx, pending, blob.Blob{Team: intent.Identity.Team, Digest: intent.Digest, Size: intent.Size}); err != nil {
		return blob.Receipt{}, err
	}
	if err := s.objects.DeleteObject(ctx, pending); err != nil {
		return blob.Receipt{}, err
	}
	s.bodies[intent.Digest] = make([]byte, intent.Size)
	return blob.Receipt{Identity: intent.Identity, Digest: intent.Digest}, nil
}

type storageMaterializationRecords struct {
	blob.RecordStore
	results   map[blob.RecordKey]blob.RecordLookupResult
	hit       blob.MemoHit
	copyErr   error
	commitErr error
}

func (r *storageMaterializationRecords) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	if r.commitErr != nil {
		return blob.Record{}, r.commitErr
	}
	record := blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source}
	r.results[q.Key] = blob.RecordLookupResult{Found: true, Record: record, Members: slices.Clone(q.Members)}
	return record, nil
}

func (r *storageMaterializationRecords) LookupRecord(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
	return r.results[q.Key], nil
}

func (r *storageMaterializationRecords) CopyMemo(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error) {
	return r.hit, r.copyErr
}

func checkStorageFilesystemMaterialization(t *testing.T) {
	objects, dir := storageFilesystem(t)
	checkStorageMaterialization(t, objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}, func(key string, body []byte) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(key)), body, 0o600); err != nil {
			t.Fatal(err)
		}
	})
}

func checkStorageS3Materialization(t *testing.T) {
	objects, transport := storageS3(t)
	checkStorageMaterialization(t, objects, blob.TransferClient{Transport: transport}, func(key string, body []byte) {
		transport.objects[key] = bytes.Clone(body)
	})
}

func checkStorageMaterialization(t *testing.T, objects blob.ObjectBackend, transfers blob.HTTPClient, overwrite func(string, []byte)) {
	storageProperty(t, func(seed [16]byte, ordinal uint8) bool {
		identity, authority := storageIdentity(seed, uint32(ordinal))
		authority.Repo = "gh:1"
		authority.Ref.Scope.Ref = "feature"
		signer := &storageMaterializationSigner{storageCacheSigner: storageCacheSigner{objects: objects, team: identity.Team, bodies: make(map[blob.Digest][]byte)}, tickets: make(map[string]blob.UploadIntent)}
		store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer, HTTPClient: transfers})
		records := &storageMaterializationRecords{results: make(map[blob.RecordKey]blob.RecordLookupResult)}
		budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
		data := artifacts.Data{Bytes: store, Records: records, Authority: authority, Identity: identity, Pipeline: "build", Budget: budget}
		output := seed[:ordinal%17]
		result, err := (blob.OutputData{Bytes: store, Records: records, Authority: authority, Identity: identity, Pipeline: "build"}).Put(t.Context(), output)
		if err != nil {
			t.Fatal(err)
		}
		producer := t.TempDir()
		file := fmt.Sprintf("artifact-%x", seed[:4])
		payload := append(bytes.Clone(seed[:]), ordinal)
		if err := os.WriteFile(filepath.Join(producer, file), payload, 0o640); err != nil {
			t.Fatal(err)
		}
		manifest, err := artifacts.Capture(t.Context(), data, producer, []string{"*"}, func(warning string) { t.Fatal(warning) })
		if err != nil {
			t.Fatal(err)
		}
		if result.Key != blob.ResultRecordKey(identity.Attempt.Value, authority.Repo, authority.Ref.Scope, data.Pipeline, blob.KindOutput) || manifest.Key != blob.ResultRecordKey(identity.Attempt.Value, authority.Repo, authority.Ref.Scope, data.Pipeline, blob.KindManifest) {
			return false
		}
		members := records.results[manifest.Key].Members
		records.hit = blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: result.Digest}, Manifest: runspec.Optional[blob.Digest]{Set: true, Value: manifest.Digest}, Members: members}
		request := blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: authority, Budget: budget}, Destination: identity.Attempt.Value}
		materialized, err := blob.MaterializeMemo(t.Context(), store, records, request)
		if err != nil || !materialized.Hit.Found || !bytes.Equal(materialized.Output, output) {
			t.Fatalf("materialize: %+v, %v", materialized, err)
		}
		consumer := t.TempDir()
		if n, err := artifacts.Stage(t.Context(), data, consumer, []artifacts.Edge{{Record: manifest.Key}}); n != 1 || err != nil {
			t.Fatalf("stage: %d, %v", n, err)
		}
		if body, err := os.ReadFile(filepath.Join(consumer, file)); err != nil || !bytes.Equal(body, payload) {
			return false
		}
		faults := []string{"follower_output", "manifest_commit", "refused_edge", "missing_bytes", "corrupt_output", "corrupt_manifest", "corrupt_member", "missing_output_record"}
		fault := faults[int(ordinal)%len(faults)]
		damaged := result.Digest
		switch fault {
		case "manifest_commit":
			records.copyErr = errors.New("manifest transaction refused")
		case "refused_edge":
			records.copyErr = blob.ErrDigestNotReadable
		case "missing_output_record":
			records.hit.Output = runspec.Optional[blob.Digest]{}
		case "missing_bytes", "corrupt_member":
			damaged = members[0].Digest
		case "corrupt_manifest":
			damaged = manifest.Digest
		}
		if records.copyErr == nil && fault != "missing_output_record" {
			key, err := blob.ObjectKey(identity.Team, damaged)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "follower_output" || fault == "missing_bytes" {
				if err := objects.DeleteObject(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			} else {
				overwrite(key, []byte("corrupt"))
			}
		}
		materialized, err = blob.MaterializeMemo(t.Context(), store, records, request)
		if err != nil || materialized.Hit.Found || len(materialized.Output) != 0 {
			t.Fatalf("%s became a memo hit: %+v, %v", fault, materialized, err)
		}
		if fault == "missing_bytes" || fault == "corrupt_member" || fault == "corrupt_manifest" {
			if err := os.WriteFile(filepath.Join(consumer, file), []byte("preserved"), 0o640); err != nil {
				t.Fatal(err)
			}
			if n, err := artifacts.Stage(t.Context(), data, consumer, []artifacts.Edge{{Record: manifest.Key}}); n != 0 || err == nil {
				t.Fatalf("%s staged: %d, %v", fault, n, err)
			}
			if body, err := os.ReadFile(filepath.Join(consumer, file)); err != nil || string(body) != "preserved" {
				return false
			}
		}
		return true
	})
}
