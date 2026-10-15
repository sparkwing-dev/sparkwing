package acceptance_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type storageCacheRecords struct {
	blob.RecordStore
	result blob.RecordLookupResult
	lookup func(blob.RecordLookup)
}

func (s storageCacheRecords) LookupRecord(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
	s.lookup(q)
	return s.result, nil
}

type storageCacheSigner struct {
	blob.DataSigner
	objects blob.ObjectBackend
	team    string
	bodies  map[blob.Digest][]byte
}

func (s storageCacheSigner) Download(ctx context.Context, q blob.GetRequest) (blob.SignedTransfer, error) {
	key, err := blob.ObjectKey(s.team, q.Digest)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	object, err := s.objects.Sign(ctx, capability.ObjectToSign{
		Method: http.MethodGet, Key: key,
		Size: int64(len(s.bodies[q.Digest])), SHA256: string(q.Digest), ExpiresAt: time.Now().Add(time.Minute),
	})
	return blob.SignedTransfer{URL: object.URL, Headers: object.Headers, Digest: q.Digest, Size: object.Size, ExpiresAt: object.ExpiresAt}, err
}

func checkStorageRemoteCache(t *testing.T) {
	storageProperty(t, func(seed [16]byte, ordinal uint8) bool {
		objects, dir := storageFilesystem(t)
		i, _ := storageIdentity(seed, uint32(ordinal))
		repo := runspec.RepoKey("gh:" + hex.EncodeToString(seed[:]))
		payload := append(bytes.Clone(seed[:]), ordinal)
		output, artifact := storageDigest(seed[:]), storageDigest(payload)
		manifestBytes, err := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{{Path: "out", Digest: artifact, Size: int64(len(payload)), Mode: 0o644}}})
		if err != nil {
			t.Fatal(err)
		}
		manifest := storageDigest(manifestBytes)
		bodies := map[blob.Digest][]byte{output: seed[:], artifact: payload, manifest: manifestBytes}
		for digest, body := range bodies {
			key, err := blob.ObjectKey(i.Team, digest)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, key)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		store := blob.NewSignedStore(blob.SignedStoreOptions{
			Signer: storageCacheSigner{objects: objects, team: i.Team, bodies: bodies}, HTTPClient: blob.TransferClient{Transport: storageFilesystemHTTP(objects)},
		})
		if _, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{}, store); err == nil {
			return false
		}
		budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
		for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
			key := blob.RecordKey{Team: i.Team, Kind: kind, Name: "cache", Scope: blob.Scope{
				Repo: repo,
				Ref:  blob.RefScope{Ref: "main", Verified: true}, Key: string(output),
			}}
			if kind == blob.KindMemo {
				key.Name, key.Scope.Pipeline, key.Scope.Job = blob.MemoRecordName, "build", i.Attempt.Value.NodeID
			}
			result := blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: output, Source: i, RefFacts: blob.RefFacts{
				Scope: key.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: hex.EncodeToString(seed[:]),
			}}}
			if kind == blob.KindMemo {
				result.Record.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest}
				result.Members = []blob.Member{{Team: i.Team, ManifestDigest: manifest, Digest: artifact, Path: "out", Size: int64(len(payload)), Mode: 0o644}}
			}
			faults := []string{"", "team", "repo", "kind", "ref", "manual", "default", "delivery", "sha", "pipeline", "job", "key", "name", "member", "corrupt_output", "missing_output"}
			if kind == blob.KindMemo {
				faults = append(faults, "corrupt_manifest", "corrupt_member")
			}
			for _, fault := range faults {
				bad := storageCacheMutation(result, fault)
				cache := storageOpenCache(t, store, repo, i.Team, bad)
				restore := storageDamageCache(t, dir, i.Team, bodies, map[string]blob.Digest{
					"corrupt_output": output, "missing_output": output, "corrupt_manifest": manifest, "corrupt_member": artifact,
				}[fault], fault == "missing_output")
				found := storageCacheFound(t.Context(), cache, key, budget)
				restore()
				if found != (fault == "") {
					t.Fatalf("%s %s: found=%t", kind, fault, found)
				}
				allowed := []blob.Digest{output}
				if kind == blob.KindMemo {
					allowed = append(allowed, manifest, artifact)
				}
				if !storageCacheAccess(t, cache, allowed, bodies, found) {
					return false
				}
				if _, err := cache.Artifact(t.Context(), storageDigest(append(payload, 0))); !errors.Is(err, blob.ErrDigestNotReadable) {
					return false
				}
			}
		}
		return true
	})
}

func storageCacheMutation(result blob.RecordLookupResult, fault string) blob.RecordLookupResult {
	bad := result
	bad.Members = slices.Clone(result.Members)
	switch fault {
	case "team":
		bad.Record.Key.Team += "-other"
	case "repo":
		bad.Record.Key.Scope.Repo += "-other"
	case "kind":
		bad.Record.Key.Kind = blob.KindBinary
	case "ref":
		bad.Record.Key.Scope.Ref.Ref = "feature"
	case "manual":
		bad.Record.RefFacts.Scope.Verified = false
	case "default":
		bad.Record.RefFacts.DefaultBranchVerified = false
	case "delivery":
		bad.Record.RefFacts.AppDeliveryKey = ""
	case "sha":
		bad.Record.RefFacts.SHA = ""
	case "pipeline":
		bad.Record.Key.Scope.Pipeline += "-other"
	case "job":
		bad.Record.Key.Scope.Job += "-other"
	case "key":
		bad.Record.Key.Scope.Key += "-other"
	case "name":
		bad.Record.Key.Name += "-other"
	case "member":
		bad.Members = []blob.Member{{Team: result.Record.Key.Team + "-other", Digest: result.Record.Digest}}
	}
	return bad
}

func storageOpenCache(t *testing.T, store blob.Store, repo runspec.RepoKey, team string, result blob.RecordLookupResult) blob.RemoteCache {
	t.Helper()
	records := storageCacheRecords{result: result, lookup: func(q blob.RecordLookup) {
		if q.Key.Team != team || !q.VerifiedDefaultOnly || q.Authority.CapabilityRef != "user-read" || q.Authority.Repo != repo || q.Key.Scope.Ref != (blob.RefScope{}) {
			t.Fatalf("unscoped remote lookup: %+v", q)
		}
	}}
	cache, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{Profile: "ci", Team: team, Authority: blob.Authority{CapabilityRef: "user-read", Principal: "reader", Repo: repo}, Repo: repo, Records: records}, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Artifact(t.Context(), result.Record.Digest); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("artifact readable before cache lookup")
	}
	return cache
}

func storageDamageCache(t *testing.T, dir, team string, bodies map[blob.Digest][]byte, digest blob.Digest, missing bool) func() {
	t.Helper()
	if digest == "" {
		return func() {}
	}
	key, err := blob.ObjectKey(team, digest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, key)
	if missing {
		err = os.Remove(path)
	} else {
		body := bytes.Clone(bodies[digest])
		body[0] ^= 1
		err = os.WriteFile(path, body, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.WriteFile(path, bodies[digest], 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func storageCacheFound(ctx context.Context, cache blob.RemoteCache, key blob.RecordKey, budget blob.Budget) bool {
	found := false
	if key.Kind == blob.KindMemo {
		hit, err := cache.Memo(ctx, blob.MemoKey{Repo: key.Scope.Repo, Ref: blob.RefScope{Ref: "feature"}, Pipeline: key.Scope.Pipeline, Job: key.Scope.Job, Hash: runspec.Hash(key.Scope.Key)}, budget)
		found = err == nil && hit.Found
	} else {
		record, err := cache.Depcache(ctx, blob.DepcacheKey{Repo: key.Scope.Repo, Ref: blob.RefScope{Ref: "feature"}, Name: key.Name, Key: key.Scope.Key}, budget)
		found = err == nil && record.Digest != ""
	}
	return found
}

func storageCacheAccess(t *testing.T, cache blob.RemoteCache, allowed []blob.Digest, bodies map[blob.Digest][]byte, found bool) bool {
	t.Helper()
	for _, digest := range allowed {
		r, err := cache.Artifact(t.Context(), digest)
		if !found {
			if !errors.Is(err, blob.ErrDigestNotReadable) {
				return false
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(r)
		if errors.Join(readErr, r.Close()) != nil || !r.Verified() || !bytes.Equal(body, bodies[digest]) {
			return false
		}
	}
	return true
}
