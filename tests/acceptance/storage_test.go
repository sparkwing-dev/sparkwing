package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStorage(t *testing.T) {
	t.Run("storage-reference-edge-laundering/filesystem-store", func(t *testing.T) {
		objects, _ := storageFilesystem(t)
		if err := checkBlobUploadIsolation(t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("storage-reference-edge-laundering/s3-store", func(t *testing.T) {
		objects, transport := storageS3(t)
		if err := checkBlobUploadIsolation(t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: transport}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("storage-local-remote-cache-opt-in/store-data-sqlite", func(t *testing.T) {
		if err := checkBlobRemoteCacheEdges(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("storage-reachability-and-retention-tails/filesystem-store", checkStorageFilesystemCollection)
	t.Run("storage-reachability-and-retention-tails/s3-store-boundary", checkStorageS3Collection)
	t.Run("storage-reference-edge-laundering/filesystem-manifests", checkStorageFilesystemManifests)
	t.Run("storage-reference-edge-laundering/s3-manifests", checkStorageS3Manifests)
	t.Run("storage-reference-edge-laundering/filesystem-manifest-records", checkStorageFilesystemManifestRecords)
	t.Run("storage-reference-edge-laundering/s3-manifest-records", checkStorageS3ManifestRecords)
	t.Run("storage-log-segments-and-fallback/filesystem-records", checkStorageFilesystemLogRecords)
	t.Run("storage-log-segments-and-fallback/s3-records", checkStorageS3Logs)
	t.Run("storage-log-segments-and-fallback/filesystem-sink", checkStorageFilesystemLogSink)
	t.Run("storage-log-segments-and-fallback/s3-sink", checkStorageS3LogSink)
	t.Run("storage-memo-materialization-failures/filesystem-components", checkStorageFilesystemMaterialization)
	t.Run("storage-memo-materialization-failures/s3-components", checkStorageS3Materialization)
	t.Run("storage-memo-and-depcache-scope/filesystem-archives", checkStorageFilesystemDepcache)
	t.Run("storage-memo-and-depcache-scope/s3-archives", checkStorageS3Depcache)
	t.Run("storage-publish-one-way/filesystem-components", checkStorageFilesystemPublication)
	t.Run("storage-publish-one-way/s3-components", checkStorageS3Publication)
	pendingScenarios(t, "storage")
}
