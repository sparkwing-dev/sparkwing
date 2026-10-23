package acceptance_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func checkStorageFilesystemDepcache(t *testing.T) {
	recordLeaf(t)
	storageProperty(t, func(seed [16]byte) bool {
		objects, dir := storageFilesystem(t)
		err := checkBlobDepcache(t, t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}, func(key string) error {
			path := filepath.Join(dir, filepath.FromSlash(key))
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			raw[len(raw)-1] ^= seed[0] | 1
			return os.WriteFile(path, raw, 0o600)
		}, seed)
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
}

func checkStorageS3Depcache(t *testing.T) {
	recordLeaf(t)
	storageProperty(t, func(seed [16]byte) bool {
		objects, transport := storageS3(t)
		err := checkBlobDepcache(t, t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: transport}, func(key string) error {
			raw := bytes.Clone(transport.objects[key])
			raw[len(raw)-1] ^= seed[0] | 1
			transport.objects[key] = raw
			return nil
		}, seed)
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
}
