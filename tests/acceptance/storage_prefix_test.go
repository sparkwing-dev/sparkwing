package acceptance_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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
				data := &bincache.SourceData{Bytes: store, Records: records, Authority: authority, Identity: identity}
				file := filepath.Join(t.TempDir(), "snapshot.bundle")
				if err := os.WriteFile(file, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				record, err := bincache.UploadSourceBundle(t.Context(), data, strings.Repeat("a", 40), file)
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
