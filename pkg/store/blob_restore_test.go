package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func blobRestoreFixture(t *testing.T) (*Store, *blobRecords, blob.UploadIntent) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	intent.Authority.Ref = blob.RefFacts{}
	request := runspec.SealedRequest{Version: 1, Request: runspec.Request{
		Git:         runspec.Git{Ref: "feature", SHA: "feature-sha", DefaultBranch: runspec.Optional[string]{Set: true, Value: "main"}, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Ref: "feature", SHA: "feature-sha", DeliveryKey: "feature-delivery", DefaultBranch: "main"}}},
		PullRequest: runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{BaseRepo: "gh:1", BaseRef: "release"}},
	}}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE runs SET request_json=? WHERE team='a' AND id='run'", body)
	if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix=?", intent.Authority.CapabilityRef).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var record CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.Data.ReadRefs = []string{"feature", "release", "main"}
	record.Value.Data.WriteRef = "feature"
	body, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,ref_scope='feature' WHERE prefix=?", body, intent.Authority.CapabilityRef)
	return s, records, intent
}

// safety: planted records exercise persisted provenance independently of the writer's validation.
func plantBlobRestore(t *testing.T, s *Store, records *blobRecords, intent blob.UploadIntent, kind blob.Kind, ref string, facts blob.RefFacts, manifest bool) blob.RecordKey {
	t.Helper()
	key := blob.RecordKey{Team: "a", Kind: kind, Scope: blob.Scope{Repo: "gh:1", Ref: blob.RefScope{Ref: ref, Verified: true}, Key: fixtureMemoHash}, Name: "modules"}
	if kind == blob.KindMemo {
		key.Scope.Pipeline, key.Scope.Job, key.Name = "ci", "job", "output"
	}
	namespace, err := blob.RecordNamespace(key)
	if err != nil {
		t.Fatal(err)
	}
	record := blob.Record{Key: key, Digest: intent.Digest, Source: intent.Identity, Uploader: "machine", Provenance: blob.ProvenanceLocal, RefFacts: facts, CreatedAt: s.now()}
	if manifest {
		record.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: intent.Digest}
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return persistBlobRecordTx(t.Context(), tx, record, namespace, intent.Identity, nil)
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func restoreFacts(ref string, isDefault bool) blob.RefFacts {
	return blob.RefFacts{Scope: blob.RefScope{Ref: ref, Verified: true}, SHA: ref + "-sha", AppDeliveryKey: ref + "-delivery", DefaultBranch: "main", DefaultBranchVerified: isDefault}
}

func TestBlobRecordsDepcacheRestoreOrderAndVerifiedDefault(t *testing.T) {
	s, records, intent := blobRestoreFixture(t)
	request := blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Ref: blob.RefScope{Ref: "feature", Verified: true}, Name: "modules", Key: fixtureMemoHash}}
	check := func(want blob.RestoreSource) {
		t.Helper()
		hit, err := records.LookupDepcache(t.Context(), request)
		if err != nil || hit.Found != (want != "") || hit.Source != want {
			t.Fatalf("restore=%+v, want %q: %v", hit, want, err)
		}
	}
	plantBlobRestore(t, s, records, intent, blob.KindDepcache, "main", restoreFacts("main", false), false)
	check("")
	plantBlobRestore(t, s, records, intent, blob.KindDepcache, "main", restoreFacts("main", true), false)
	check(blob.RestoreDefaultBranch)
	plantBlobRestore(t, s, records, intent, blob.KindDepcache, "release", restoreFacts("release", false), false)
	check(blob.RestorePRBase)
	plantBlobRestore(t, s, records, intent, blob.KindDepcache, "feature", restoreFacts("feature", false), false)
	check(blob.RestoreOwnRef)
	request.Key.Ref = blob.RefScope{Ref: "main", Verified: true}
	if _, err := records.LookupDepcache(t.Context(), request); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("caller selected default scope: %v", err)
	}
}

func TestBlobRecordsMemoOwnScopePrecedesDefaultWithoutPRBaseFallback(t *testing.T) {
	s, records, intent := blobRestoreFixture(t)
	request := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: blob.RefScope{Ref: "feature", Verified: true}, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
	plantBlobRestore(t, s, records, intent, blob.KindMemo, "release", restoreFacts("release", false), false)
	if hit, err := records.LookupMemo(t.Context(), request); err != nil || hit.Found {
		t.Fatalf("memo used PR-base fallback: %+v: %v", hit, err)
	}
	plantBlobRestore(t, s, records, intent, blob.KindMemo, "main", restoreFacts("main", true), false)
	hit, err := records.LookupMemo(t.Context(), request)
	if err != nil || !hit.Found || len(hit.ReadSet.Edges) == 0 || hit.ReadSet.Edges[0].Record.Scope.Ref.Ref != "main" {
		t.Fatalf("verified default memo=%+v: %v", hit, err)
	}
	plantBlobRestore(t, s, records, intent, blob.KindMemo, "feature", restoreFacts("feature", false), false)
	hit, err = records.LookupMemo(t.Context(), request)
	if err != nil || !hit.Found || len(hit.ReadSet.Edges) == 0 || hit.ReadSet.Edges[0].Record.Scope.Ref.Ref != "feature" {
		t.Fatalf("own memo=%+v: %v", hit, err)
	}
}

func TestBlobMemoStableFallbackCompletesProperty(t *testing.T) {
	for _, copyMemo := range []bool{false, true} {
		for valid := -1; valid < 4; valid++ {
			t.Run(fmt.Sprintf("copy=%v/valid=%d", copyMemo, valid), func(t *testing.T) {
				s, records, intent := blobRestoreFixture(t)
				var digests []blob.Digest
				for i := range 4 {
					object := intent
					object.Digest = blob.Digest(fmt.Sprintf("%064x", i+1))
					digests = append(digests, object.Digest)
					rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", object.Digest)
					ref := "feature"
					if i >= 2 {
						ref = "main"
					}
					key := plantBlobRestore(t, s, records, object, blob.KindMemo, ref, restoreFacts(ref, ref == "main"), true)
					if i%2 == 0 {
						namespace, err := blob.RecordNamespace(key)
						if err != nil {
							t.Fatal(err)
						}
						rewriteSchemaExec(t, s.db, "UPDATE blob_records SET name='manifest' WHERE kind='memo' AND scope=? AND name='output'", namespace)
					}
				}
				// safety: lookup order is output then manifest in each namespace.
				digests[0], digests[1], digests[2], digests[3] = digests[1], digests[0], digests[3], digests[2]
				records.manifests = blobManifestFunc(func(_ context.Context, _ Team, digest blob.Digest) ([]blob.Member, error) {
					if valid >= 0 && digest == digests[valid] {
						return nil, nil
					}
					if strings.HasSuffix(string(digest), "1") || strings.HasSuffix(string(digest), "3") {
						return nil, blob.ErrNotFound
					}
					return nil, blob.ErrIntegrity
				})
				request := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: blob.RefScope{Ref: "feature", Verified: true}, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
				var hit blob.MemoHit
				var err error
				if copyMemo {
					hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: request, Destination: intent.Identity.Attempt.Value})
				} else {
					hit, err = records.LookupMemo(t.Context(), request)
				}
				if err != nil || hit.Found != (valid >= 0) || valid >= 0 && hit.Manifest.Value != digests[valid] {
					t.Fatalf("stable fallback=%+v: %v", hit, err)
				}
			})
		}
	}
}

func TestBlobFallbackPendingCandidatePrecedesUnusedRecordFailure(t *testing.T) {
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		t.Run(string(kind), func(t *testing.T) {
			s, records, intent := blobRestoreFixture(t)
			records.manifests = fakeBlobManifests{intent.Digest: nil}
			key := plantBlobRestore(t, s, records, intent, kind, "feature", restoreFacts("feature", false), true)
			other := intent
			other.Digest = blob.Digest(strings.Repeat("f", 64))
			rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", other.Digest)
			fallback := plantBlobRestore(t, s, records, other, kind, "main", restoreFacts("main", true), true)
			namespace, err := blob.RecordNamespace(fallback)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE blob_records SET ref_facts_json=? WHERE kind=? AND scope=?", []byte("invalid"), kind, namespace)
			if kind == blob.KindMemo {
				request := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: key.Scope.Ref, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
				hit, err := records.LookupMemo(t.Context(), request)
				if err != nil || !hit.Found {
					t.Fatalf("pending own-ref memo lost priority: %+v: %v", hit, err)
				}
				hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: request, Destination: intent.Identity.Attempt.Value})
				if err != nil || !hit.Found {
					t.Fatalf("pending own-ref memo copy lost priority: %+v: %v", hit, err)
				}
			} else {
				hit, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Ref: key.Scope.Ref, Name: "modules", Key: fixtureMemoHash}})
				if err != nil || !hit.Found || hit.Source != blob.RestoreOwnRef {
					t.Fatalf("pending own-ref depcache lost priority: %+v: %v", hit, err)
				}
			}
		})
	}
}

func TestBlobRecordsFallbackFiltersBeforeReadingManifest(t *testing.T) {
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		t.Run(string(kind), func(t *testing.T) {
			s, records, intent := blobRestoreFixture(t)
			called := false
			records.manifests = blobManifestFunc(func(context.Context, Team, blob.Digest) ([]blob.Member, error) {
				called = true
				return nil, errors.New("manifest unavailable")
			})
			for _, facts := range []blob.RefFacts{restoreFacts("main", false), restoreFacts("release", false), {Scope: blob.RefScope{Ref: "main", Verified: true}, DefaultBranch: "main", DefaultBranchVerified: true}} {
				plantBlobRestore(t, s, records, intent, kind, "main", facts, true)
				var found bool
				var err error
				if kind == blob.KindMemo {
					var hit blob.MemoHit
					hit, err = records.LookupMemo(t.Context(), blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: blob.RefScope{Ref: "feature", Verified: true}, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}})
					found = hit.Found
				} else {
					var hit blob.DepcacheHit
					hit, err = records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Ref: blob.RefScope{Ref: "feature", Verified: true}, Name: "modules", Key: fixtureMemoHash}})
					found = hit.Found
				}
				if err != nil || found || called {
					t.Fatalf("excluded fallback read bytes: found=%v called=%v: %v", found, called, err)
				}
			}
		})
	}
}

func TestBlobRecordsExpiredCacheBudgetsRunCold(t *testing.T) {
	s, records, intent := blobRestoreFixture(t)
	key := plantBlobRestore(t, s, records, intent, blob.KindMemo, "feature", restoreFacts("feature", false), false)
	budget := blob.Budget{Deadline: s.now().Add(-time.Nanosecond)}
	hit, err := records.LookupMemo(t.Context(), blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: key.Scope.Repo, Ref: key.Scope.Ref, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}, Budget: budget})
	if err != nil || hit.Found {
		t.Fatalf("expired memo budget=%+v: %v", hit, err)
	}
	plantBlobRestore(t, s, records, intent, blob.KindDepcache, "feature", restoreFacts("feature", false), false)
	dep, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Ref: key.Scope.Ref, Name: "modules", Key: fixtureMemoHash}, Budget: budget})
	if err != nil || dep.Found {
		t.Fatalf("expired depcache budget=%+v: %v", dep, err)
	}
}

func TestBlobRecordsReadSetEnforcesCacheFallbackProvenance(t *testing.T) {
	formerDefault := restoreFacts("release", true)
	formerDefault.DefaultBranch = "release"
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		for _, test := range []struct {
			name, ref string
			facts     blob.RefFacts
			allowed   bool
		}{
			{"own", "feature", restoreFacts("feature", false), true},
			{"pr_base", "release", restoreFacts("release", false), kind == blob.KindDepcache},
			{"pr_base_former_default", "release", formerDefault, kind == blob.KindDepcache},
			{"unproven_default", "main", restoreFacts("main", false), false},
			{"default", "main", restoreFacts("main", true), true},
		} {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				s, records, intent := blobRestoreFixture(t)
				intent.Digest = blob.Digest(strings.Repeat("b", 64))
				rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", intent.Digest)
				plantBlobRestore(t, s, records, intent, kind, test.ref, test.facts, false)
				presence, err := records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: intent.Authority, Digests: []blob.Digest{intent.Digest}})
				if err != nil || (len(presence.Present) == 1) != test.allowed {
					t.Errorf("cache presence=%+v, allowed=%v: %v", presence, test.allowed, err)
				}
				principal := dataAccessAuthority(t, s, intent.Authority.CapabilityRef).Principal
				for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
					err := records.tenant.CheckDataAccess(t.Context(), capability.Authorized{Principal: principal}, capability.DataTarget{Operation: operation, Digest: string(intent.Digest)})
					if test.allowed && err != nil || !test.allowed && !errors.Is(err, blob.ErrDigestNotReadable) {
						t.Errorf("%s allowed=%v: %v", operation, test.allowed, err)
					}
				}
				request := blobOutputRequest(intent)
				request.Key.Scope.Ref = blob.RefScope{Ref: "feature", Verified: true}
				_, err = records.CommitRecord(t.Context(), request)
				if test.allowed && err != nil || !test.allowed && !errors.Is(err, blob.ErrDigestNotReadable) {
					t.Fatalf("cache reference commit allowed=%v: %v", test.allowed, err)
				}
			})
		}
	}
}
