package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"testing/synctest"
	"time"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// safety: component proofs do not replace the native target's transaction and transport evidence.
func storageBoundaryChecks() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"storage-cas-integrity":                          checkStorageCAS,
		"storage-reference-edge-laundering":              checkStorageReceipts,
		"storage-memo-and-depcache-scope":                checkStorageNamespaces,
		"storage-binary-provenance":                      checkStorageBinaryTrust,
		"storage-owner-receipts-and-stream-verification": checkStorageOwnerReceipts,
		"storage-hasmany-large-budget":                   checkStoragePresence,
		"storage-memo-materialization-failures":          checkStorageMemoFailures,
		"storage-local-remote-cache-opt-in":              checkStorageRemoteCache,
	}
}

func checkStorageCAS(t *testing.T) {
	t.Run("streams", checkStorageStreams)
	t.Run("filesystem", checkStorageFilesystemCAS)
	t.Run("signatures", checkStorageFilesystemSignatures)
}

func storageProperty(t *testing.T, property any) {
	t.Helper()
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}

func storageDigest(body []byte) blob.Digest {
	sum := sha256.Sum256(body)
	return blob.Digest(hex.EncodeToString(sum[:]))
}

func storageIdentity(seed [16]byte, ordinal uint32) (blob.UploadIdentity, blob.Authority) {
	id := hex.EncodeToString(seed[:])
	i := blob.UploadIdentity{
		Kind: blob.UploadJob, Team: "team-" + id, RunID: "run-" + id,
		Principal: blob.Principal("principal-" + id), CapabilityRef: "cap-" + id,
		Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{
			Team: "team-" + id, RunID: "run-" + id, NodeID: "job-" + id,
			Attempt: int64(ordinal) + 1, Generation: int64(ordinal) + 2,
		}},
	}
	return i, blob.Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Attempt: i.Attempt}
}

func checkStorageReceipts(t *testing.T) {
	// safety: a receipt never transfers between identities, even when the CAS digest is identical.
	storageProperty(t, func(seed [16]byte, ordinal uint32) bool {
		i, a := storageIdentity(seed, ordinal)
		digest := storageDigest(seed[:])
		q := blob.EdgeRequest{
			TargetKind: blob.KindManifest, Authority: a, Digest: digest,
			OwnReceipt: runspec.Optional[blob.Receipt]{Set: true, Value: blob.Receipt{Identity: i, Digest: digest}},
		}
		if blob.AuthorizeEdge(q) != nil {
			return false
		}
		for _, mutate := range []func(*blob.UploadIdentity){
			func(i *blob.UploadIdentity) { i.Team += "-other" },
			func(i *blob.UploadIdentity) { i.RunID += "-other" },
			func(i *blob.UploadIdentity) { i.Attempt.Value.Team += "-other" },
			func(i *blob.UploadIdentity) { i.Attempt.Value.RunID += "-other" },
			func(i *blob.UploadIdentity) { i.Attempt.Value.NodeID += "-other" },
			func(i *blob.UploadIdentity) { i.Attempt.Value.Attempt++ },
			func(i *blob.UploadIdentity) { i.Attempt.Value.Generation++ },
			func(i *blob.UploadIdentity) { i.Principal += "-other" },
			func(i *blob.UploadIdentity) { i.CapabilityRef += "-other" },
		} {
			foreign := q
			mutate(&foreign.OwnReceipt.Value.Identity)
			if !errors.Is(blob.AuthorizeEdge(foreign), blob.ErrDigestNotReadable) {
				return false
			}
		}
		q.OwnReceipt.Set = false
		if !errors.Is(blob.AuthorizeEdge(q), blob.ErrDigestNotReadable) {
			return false
		}
		for _, kind := range []blob.EdgeKind{blob.EdgeDependency, blob.EdgeDeclaredReference, blob.EdgeMemoHit, blob.EdgeMemoScope, blob.EdgeOwnUpload} {
			q.ReadSet = blob.ReadSet{Reader: i.Attempt.Value, Edges: []blob.ReadEdge{{Kind: kind, Reader: i.Attempt.Value, Digest: digest}}}
			if blob.AuthorizeEdge(q) != nil {
				return false
			}
			q.ReadSet.Edges[0].Reader.NodeID += "-other"
			if !errors.Is(blob.AuthorizeEdge(q), blob.ErrDigestNotReadable) {
				return false
			}
		}
		return true
	})
}

func checkStorageOwnerReceipts(t *testing.T) {
	// safety: an owner-unit receipt retains its unit fence and grants no unrelated job output edge.
	storageProperty(t, func(seed [16]byte, ordinal uint32) bool {
		job, a := storageIdentity(seed, ordinal)
		for _, kind := range []blob.UploadUnitKind{blob.UploadTriggerUnit, blob.UploadPlanUnit, blob.UploadEvalUnit} {
			i := job
			i.Kind, i.Attempt = blob.UploadOwnerUnit, runspec.Optional[runspec.AttemptIdentity]{}
			i.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: kind, ID: "unit", Generation: int64(ordinal) + 1}}
			if kind == blob.UploadEvalUnit {
				i.Unit.Value.Source = job.Attempt
			}
			owner := a
			owner.Attempt, owner.Upload = i.Attempt, runspec.Optional[blob.UploadIdentity]{Set: true, Value: i}
			for _, recordKind := range []blob.Kind{blob.KindBinary, blob.KindSourceBundle} {
				if blob.AuthorizeUpload(owner, i, recordKind) != nil {
					return false
				}
				altered := i
				altered.Unit.Value.Generation++
				if !errors.Is(blob.AuthorizeUpload(owner, altered, recordKind), blob.ErrDigestNotReadable) {
					return false
				}
			}
			digest := storageDigest(seed[:])
			q := blob.EdgeRequest{
				TargetKind: blob.KindOutput, Authority: a, Digest: digest,
				OwnReceipt: runspec.Optional[blob.Receipt]{Set: true, Value: blob.Receipt{Identity: i, Digest: digest}},
			}
			if !errors.Is(blob.AuthorizeEdge(q), blob.ErrDigestNotReadable) || blob.AuthorizeUpload(owner, i, blob.KindOutput) == nil {
				return false
			}
		}
		return true
	})
	checkStorageStreams(t)
}

func checkStorageNamespaces(t *testing.T) {
	// safety: component boundaries and the verified-ref tag cannot alias another cache namespace.
	storageProperty(t, func(parts [5]string) bool {
		for i, part := range parts {
			parts[i] = "x../%2f:\\:manual:" + strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return '_'
				}
				return r
			}, part)
		}
		key := blob.MemoKey{Repo: runspec.RepoKey(parts[0]), Ref: blob.RefScope{Ref: parts[1]}, Pipeline: parts[2], Job: parts[3], Hash: runspec.Hash(storageDigest([]byte(parts[4])))}
		manual, err := blob.MemoNamespace(key)
		if err != nil {
			return false
		}
		key.Ref.Verified = true
		verified, err := blob.MemoNamespace(key)
		if err != nil || manual == verified {
			return false
		}
		key.Ref.Ref = "manual:" + parts[1]
		literal, err := blob.MemoNamespace(key)
		if err != nil || literal == manual {
			return false
		}
		key.Pipeline, key.Job = parts[2]+"/"+parts[3], parts[4]
		left, err := blob.MemoNamespace(key)
		if err != nil {
			return false
		}
		key.Pipeline, key.Job = parts[2], parts[3]+"/"+parts[4]
		right, err := blob.MemoNamespace(key)
		if err != nil || left == right {
			return false
		}
		dep := blob.DepcacheKey{Repo: key.Repo, Ref: key.Ref, Name: parts[2] + "/" + parts[3], Key: parts[4]}
		left, err = blob.DepcacheNamespace(dep)
		if err != nil {
			return false
		}
		dep.Name, dep.Key = parts[2], parts[3]+"/"+parts[4]
		right, err = blob.DepcacheNamespace(dep)
		return err == nil && left != right
	})
}

func checkStorageBinaryTrust(t *testing.T) {
	// safety: executable trust requires the exact committed digest, tenant and server provenance.
	storageProperty(t, func(seed [16]byte, optIn bool) bool {
		digest := storageDigest(seed[:])
		for _, provenance := range []blob.Provenance{blob.ProvenanceCloud, blob.ProvenanceLocal, "", "caller-selected"} {
			record := blob.Record{Key: blob.RecordKey{Team: "team", Kind: blob.KindBinary}, Digest: digest, Provenance: provenance}
			execution := blob.BinaryExecution{Record: record, Digest: digest, Trust: blob.BuildTrust{Team: "team", TrustLocalBuilds: optIn}}
			allowed := provenance == blob.ProvenanceCloud || (provenance == blob.ProvenanceLocal && optIn)
			if (blob.CheckBinaryExecution(execution) == nil) != allowed {
				return false
			}
			execution.Trust.Team = "foreign-team"
			if !errors.Is(blob.CheckBinaryExecution(execution), blob.ErrDigestNotReadable) {
				return false
			}
			execution.Trust.Team = "team"
			execution.Digest = storageDigest(append(seed[:], 0))
			if !errors.Is(blob.CheckBinaryExecution(execution), blob.ErrIntegrity) {
				return false
			}
		}
		return true
	})
}

type storageDownload struct {
	blob.DataSigner
	transfer blob.SignedTransfer
	err      error
}

func (s storageDownload) Download(context.Context, blob.GetRequest) (blob.SignedTransfer, error) {
	return s.transfer, s.err
}

type storageTransfer struct{ body []byte }

func (s storageTransfer) Transfer(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func checkStorageStreams(t *testing.T) {
	// safety: successful verification requires intact bytes through EOF, even at an exact Content-Length.
	storageProperty(t, func(seed [64]byte, length uint8) bool {
		body := seed[:int(length)%len(seed)+1]
		digest := storageDigest(body)
		changed := bytes.Clone(body)
		changed[0] ^= 1
		for _, data := range [][]byte{body, changed, body[:len(body)-1], append(bytes.Clone(body), 0)} {
			store := blob.NewSignedStore(blob.SignedStoreOptions{
				Signer:     storageDownload{transfer: blob.SignedTransfer{Digest: digest, Size: int64(len(body))}},
				HTTPClient: storageTransfer{body: data},
			})
			r, err := store.Get(t.Context(), blob.GetRequest{Digest: digest})
			if err != nil {
				return false
			}
			got, readErr := io.ReadAll(r)
			closeErr := r.Close()
			if bytes.Equal(data, body) {
				if readErr != nil || closeErr != nil || !r.Verified() || !bytes.Equal(got, body) {
					return false
				}
			} else if !errors.Is(readErr, blob.ErrIntegrity) || closeErr != nil || r.Verified() {
				return false
			}
		}
		store := blob.NewSignedStore(blob.SignedStoreOptions{
			Signer:     storageDownload{transfer: blob.SignedTransfer{Digest: digest, Size: int64(len(body))}},
			HTTPClient: storageTransfer{body: body},
		})
		r, err := store.Get(t.Context(), blob.GetRequest{Digest: digest})
		if err != nil {
			return false
		}
		if _, err := io.ReadFull(r, make([]byte, len(body))); err != nil {
			return false
		}
		return r.Close() == nil && !r.Verified()
	})
}

type storagePresence struct {
	blob.DataSigner
	request blob.HasManyRequest
	result  blob.Presence
	after   func()
	calls   int
}

func (s *storagePresence) HasMany(_ context.Context, q blob.HasManyRequest) (blob.Presence, error) {
	s.calls++
	s.request = q
	if s.after != nil {
		s.after()
	}
	return s.result, nil
}

func checkStoragePresence(t *testing.T) {
	var digests, readable []blob.Digest
	for i := range 4097 {
		digest := storageDigest([]byte{byte(i), byte(i >> 8)})
		digests = append(digests, digest, digest)
		if i%3 == 0 {
			readable = append(readable, digest)
		}
	}
	for _, fault := range []string{"", "expired", "cancelled", "unsolicited"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			signer := &storagePresence{result: blob.Presence{Present: append(append([]blob.Digest{}, readable...), readable...)}}
			budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
			switch fault {
			case "expired":
				budget.Deadline = time.Unix(1, 0)
			case "cancelled":
				signer.after = cancel
			case "unsolicited":
				signer.result.Present = append(signer.result.Present, storageDigest([]byte("not requested")))
			}
			store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer})
			result, err := store.HasMany(ctx, blob.HasManyRequest{Digests: digests, Budget: budget})
			if fault != "" {
				want := map[string]error{"expired": blob.ErrBudget, "cancelled": context.Canceled, "unsolicited": blob.ErrIntegrity}[fault]
				if !errors.Is(err, want) || len(result.Present) != 0 {
					t.Fatalf("partial or invalid presence accepted: %d, %v", len(result.Present), err)
				}
				if fault == "expired" && signer.calls != 0 {
					t.Fatal("expired budget reached signer")
				}
				return
			}
			if err != nil || len(result.Present) != len(readable) || signer.calls != 1 || signer.request.Budget != budget || len(signer.request.Digests) != 4097 {
				t.Fatalf("presence truncated or lost budget: %d results, %d requested, %v", len(result.Present), len(signer.request.Digests), err)
			}
			for i, digest := range result.Present {
				if digest != readable[i] {
					t.Fatal("presence changed independently supplied read-set result")
				}
			}
		})
	}
}

type storageMemo struct {
	blob.RecordStore
	hit  blob.MemoHit
	err  error
	wait bool
}

func (s storageMemo) CopyMemo(ctx context.Context, _ blob.CopyMemoRequest) (blob.MemoHit, error) {
	if s.wait {
		<-ctx.Done()
		return blob.MemoHit{}, ctx.Err()
	}
	return s.hit, s.err
}

func checkStorageMemoFailures(t *testing.T) {
	t.Run("budget", checkStorageMemoBudget)
	for _, fault := range []string{"missing_output", "record_refused", "missing_bytes", "corrupt_bytes", "short_bytes", "members_without_manifest", "valid", "explicit_empty"} {
		t.Run(fault, func(t *testing.T) {
			body := []byte(`{"answer":42}`)
			if fault == "explicit_empty" {
				body = nil
			}
			digest := storageDigest(body)
			records := storageMemo{hit: blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: digest}}}
			download := storageDownload{transfer: blob.SignedTransfer{Digest: digest, Size: int64(len(body))}}
			var wantErr error
			switch fault {
			case "missing_output":
				records.hit.Output.Set = false
			case "record_refused":
				records.err = blob.ErrDigestNotReadable
				wantErr = blob.ErrDigestNotReadable
			case "missing_bytes":
				download.err = blob.ErrNotFound
			case "corrupt_bytes":
				body = bytes.Clone(body)
				body[0] ^= 1
			case "short_bytes":
				body = body[:len(body)-1]
			case "members_without_manifest":
				records.hit.Members = []blob.Member{{Digest: digest}}
			}
			store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: download, HTTPClient: storageTransfer{body: body}})
			result, err := blob.MaterializeMemo(t.Context(), store, records, blob.CopyMemoRequest{
				Lookup: blob.MemoLookup{Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}},
			})
			if fault == "valid" || fault == "explicit_empty" {
				if err != nil || !result.Hit.Found || !bytes.Equal(result.Output, body) {
					t.Fatalf("verified memo missed: %+v, %v", result, err)
				}
			} else if !errors.Is(err, wantErr) || result.Hit.Found || result.Output != nil {
				t.Fatalf("failed materialization returned a hit: %+v, %v", result, err)
			}
		})
	}
}

func checkStorageMemoBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		result, err := blob.MaterializeMemo(t.Context(), blob.NewSignedStore(blob.SignedStoreOptions{}), storageMemo{wait: true}, blob.CopyMemoRequest{
			Lookup: blob.MemoLookup{Budget: blob.Budget{Deadline: time.Now().Add(time.Second)}},
		})
		if err != nil || result.Hit.Found || result.Output != nil {
			t.Fatalf("exhausted memo budget returned a hit: %+v, %v", result, err)
		}
	})
}
