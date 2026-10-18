package acceptance_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStoragePrefixSourceBundles(t *testing.T) {
	for _, backend := range []string{"filesystem", "s3"} {
		t.Run(backend, func(t *testing.T) {
			var objects blob.ObjectBackend
			var transfers blob.HTTPClient
			if backend == "filesystem" {
				f, _ := storageFilesystem(t)
				objects, transfers = f, blob.TransferClient{Transport: storageFilesystemHTTP(f)}
			} else {
				s, transport := storageS3(t)
				objects, transfers = s, blob.TransferClient{Transport: transport}
			}
			storageProperty(t, func(payload []byte, ordinal uint8) bool {
				identity, authority := storageIdentity([16]byte{}, uint32(ordinal))
				authority.Repo = "gh:1"
				authority.Ref.Scope.Ref = "main"
				signer := &storageMaterializationSigner{storageCacheSigner: storageCacheSigner{objects: objects, team: identity.Team, bodies: make(map[blob.Digest][]byte)}, tickets: make(map[string]blob.UploadIntent)}
				store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer, HTTPClient: transfers})
				records := &storageMaterializationRecords{results: make(map[blob.RecordKey]blob.RecordLookupResult)}
				if err := blob.AuthorizeUpload(authority, identity, blob.KindSourceBundle); err != nil {
					t.Fatal(err)
				}
				key, err := blob.ResolveWriteKey(authority, blob.WriteScopeIntent{Kind: blob.KindSourceBundle, Name: strings.Repeat("a", 40)})
				if err != nil {
					t.Fatal(err)
				}
				digest := storageDigest(payload)
				receipt, err := store.Put(t.Context(), blob.PutRequest{Identity: identity, Authority: authority, Digest: digest, Size: int64(len(payload)), Body: bytes.NewReader(payload)})
				if err != nil || receipt.Digest != digest || receipt.Identity != identity {
					t.Fatalf("source bytes: %+v %v", receipt, err)
				}
				record, err := records.CommitRecord(t.Context(), blob.CommitRecordRequest{Source: identity, Authority: authority, Key: key, Digest: digest})
				if err != nil {
					t.Fatal(err)
				}
				if record.Key.Kind != blob.KindSourceBundle || record.Digest != storageDigest(payload) || records.results[record.Key].Record != record {
					return false
				}
				r, err := store.Get(t.Context(), blob.GetRequest{Authority: authority, Digest: record.Digest})
				if err != nil {
					t.Fatal(err)
				}
				got, readErr := io.ReadAll(r)
				closeErr := r.Close()
				return readErr == nil && closeErr == nil && r.Verified() && bytes.Equal(got, payload)
			})
		})
	}
}
