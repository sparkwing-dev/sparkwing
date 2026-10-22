package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStorage(t *testing.T) {
	runCell(t, "storage-reference-edge-laundering/filesystem-store", "store-blob-components", func(t *testing.T) {
		objects, _ := storageFilesystem(t)
		if err := checkBlobUploadIsolation(t, t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "storage-reference-edge-laundering/s3-store", "store-blob-components", func(t *testing.T) {
		objects, transport := storageS3(t)
		if err := checkBlobUploadIsolation(t, t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: transport}); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "storage-local-remote-cache-opt-in/store-data-sqlite", "store-blob-components", func(t *testing.T) {
		if err := checkBlobRemoteCacheEdges(t, t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "storage-reachability-and-retention-tails/filesystem-store", "store-blob-components", checkStorageFilesystemCollection)
	runCell(t, "storage-reachability-and-retention-tails/s3-store-boundary", "store-blob-components", checkStorageS3Collection)
	runCell(t, "storage-reference-edge-laundering/filesystem-manifests", "store-blob-components", checkStorageFilesystemManifests)
	runCell(t, "storage-reference-edge-laundering/s3-manifests", "store-blob-components", checkStorageS3Manifests)
	runCell(t, "storage-reference-edge-laundering/filesystem-manifest-records", "store-blob-components", checkStorageFilesystemManifestRecords)
	runCell(t, "storage-reference-edge-laundering/s3-manifest-records", "store-blob-components", checkStorageS3ManifestRecords)
	runCell(t, "storage-memo-materialization-failures/filesystem-components", "store-blob-components", checkStorageFilesystemMaterialization)
	runCell(t, "storage-memo-materialization-failures/s3-components", "store-blob-components", checkStorageS3Materialization)
	runCell(t, "storage-depcache-scope/filesystem-archives", "store-blob-components", checkStorageFilesystemDepcache)
	runCell(t, "storage-depcache-scope/s3-archives", "store-blob-components", checkStorageS3Depcache)
	runCell(t, "storage-publish-one-way/filesystem-components", "store-blob-components", checkStorageFilesystemPublication)
	runCell(t, "storage-publish-one-way/s3-components", "store-blob-components", checkStorageS3Publication)
}
