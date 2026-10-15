package acceptance_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	depcacheblob "github.com/sparkwing-dev/sparkwing/internal/blob/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/depcachekey"
)

func checkStorageFilesystemRemoteArchives(t *testing.T) {
	objects, dir := storageFilesystem(t)
	checkStorageRemoteArchives(t, objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}, func(key string, body []byte) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(key)), body, 0o600); err != nil {
			t.Fatal(err)
		}
	})
}

func checkStorageS3RemoteArchives(t *testing.T) {
	objects, transport := storageS3(t)
	checkStorageRemoteArchives(t, objects, blob.TransferClient{Transport: transport}, func(key string, body []byte) {
		transport.objects[key] = bytes.Clone(body)
	})
}

func checkStorageRemoteArchives(t *testing.T, objects blob.ObjectBackend, transfers blob.HTTPClient, overwrite func(string, []byte)) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	storageProperty(t, func(seed [16]byte, ordinal uint8) bool {
		identity, authority := storageIdentity(seed, uint32(ordinal))
		authority.Repo = "gh:1"
		work, source := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(work, "lock"), seed[:], 0o600); err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat(seed[:], 1+int(ordinal))
		if err := os.WriteFile(filepath.Join(source, "tool"), payload, 0o640); err != nil {
			t.Fatal(err)
		}
		var archive bytes.Buffer
		if err := depcache.WriteArchive(&archive, source); err != nil {
			t.Fatal(err)
		}
		signer := &storageMaterializationSigner{storageCacheSigner: storageCacheSigner{objects: objects, team: identity.Team, bodies: make(map[blob.Digest][]byte)}, tickets: make(map[string]blob.UploadIntent)}
		store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer, HTTPClient: transfers})
		digest := storageDigest(archive.Bytes())
		if _, err := store.Put(t.Context(), blob.PutRequest{Identity: identity, Authority: authority, Digest: digest, Size: int64(archive.Len()), Body: bytes.NewReader(archive.Bytes())}); err != nil {
			t.Fatal(err)
		}
		spec := depcache.Spec{Name: "tools", Path: "tools", KeyFiles: []string{"lock"}}
		key, err := depcachekey.Files(work, spec.Name, "", spec.KeyFiles)
		if err != nil {
			t.Fatal(err)
		}
		result := blob.RecordLookupResult{Found: true, Record: blob.Record{
			Key:    blob.RecordKey{Team: identity.Team, Kind: blob.KindDepcache, Name: key, Scope: blob.Scope{Repo: authority.Repo, Ref: blob.RefScope{Ref: "main", Verified: true}, Key: key}},
			Digest: digest, Source: identity, RefFacts: blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: true}, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: string(storageDigest(seed[:]))},
		}}
		budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
		readStore := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer.storageCacheSigner, HTTPClient: transfers})
		cache := storageOpenCache(t, readStore, authority.Repo, result)
		reader, err := depcacheblob.NewRemoteReader(cache, authority.Repo, budget)
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(work, spec.Path)
		if _, err := reader.Fetch(t.Context(), key, destination); err != nil {
			t.Fatalf("signed remote restore: %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(destination, "tool")); err != nil || !bytes.Equal(got, payload) {
			return false
		}
		objectKey, err := blob.ObjectKey(identity.Team, digest)
		if err != nil {
			t.Fatal(err)
		}
		damaged := bytes.Clone(archive.Bytes())
		damaged[len(damaged)-1] ^= 1
		overwrite(objectKey, damaged)
		if _, err := reader.Fetch(t.Context(), key, destination); err == nil {
			t.Fatal("corrupt remote archive restored")
		}
		if got, err := os.ReadFile(filepath.Join(destination, "tool")); err != nil || !bytes.Equal(got, payload) {
			return false
		}
		cold := filepath.Join(t.TempDir(), "missing")
		if _, err := reader.Fetch(t.Context(), key, cold); err == nil {
			t.Fatal("corrupt remote archive published into an empty destination")
		}
		if _, err := os.Stat(cold); !errors.Is(err, os.ErrNotExist) {
			return false
		}
		overwrite(objectKey, archive.Bytes())
		for _, fault := range []string{"repo", "manual", "default", "ref", "key"} {
			bad := storageOpenCache(t, readStore, authority.Repo, storageCacheMutation(result, fault))
			badReader, err := depcacheblob.NewRemoteReader(bad, authority.Repo, budget)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := badReader.Fetch(t.Context(), key, cold); err == nil {
				t.Fatalf("%s record restored", fault)
			}
		}
		if err := objects.DeleteObject(t.Context(), objectKey); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Fetch(t.Context(), key, destination); err == nil {
			t.Fatal("missing remote archive restored")
		}
		if got, err := os.ReadFile(filepath.Join(destination, "tool")); err != nil || !bytes.Equal(got, payload) {
			return false
		}
		return checkStorageRemoteArchiveLocalSave(t, reader, spec, home, key, seed, payload)
	})
}

func checkStorageRemoteArchiveLocalSave(t *testing.T, reader depcache.Reader, spec depcache.Spec, home, key string, seed [16]byte, payload []byte) bool {
	t.Helper()
	localWork := t.TempDir()
	if err := os.WriteFile(filepath.Join(localWork, "lock"), seed[:], 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := depcache.WithRemoteReader(t.Context(), reader)
	run := depcache.Restore(ctx, spec, "job", localWork)
	local := filepath.Join(localWork, spec.Path)
	if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err := os.Mkdir(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "tool"), payload, 0o640); err != nil {
		t.Fatal(err)
	}
	localArchive := filepath.Join(home, "depcache", key+".tar.gz")
	run.Save(ctx, errors.New("job failed"))
	if _, err := os.Stat(localArchive); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	run.Save(ctx, nil)
	if _, err := os.Stat(localArchive); err != nil {
		t.Fatalf("local save after remote miss: %v", err)
	}
	if err := os.RemoveAll(local); err != nil {
		t.Fatal(err)
	}
	depcache.Restore(ctx, spec, "job", localWork)
	got, err := os.ReadFile(filepath.Join(local, "tool"))
	return err == nil && bytes.Equal(got, payload)
}
