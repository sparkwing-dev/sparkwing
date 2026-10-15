package acceptance_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestStorage(t *testing.T) {
	if len(os.Args) >= 5 && os.Args[len(os.Args)-4] == "--" && os.Args[len(os.Args)-3] == "store-upload-crash" {
		s, err := acceptance.DriveStoreUploadCrash(t.Context(), os.Args[len(os.Args)-2], os.Args[len(os.Args)-1])
		if err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(s)
		os.Exit(acceptance.StoreUploadCrashExit)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	crash := func(ctx context.Context, root, point string) error {
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStorage$", "--", "store-upload-crash", root, point)
		output, err := cmd.CombinedOutput()
		var exited *exec.ExitError
		if errors.As(err, &exited) && exited.ExitCode() == acceptance.StoreUploadCrashExit {
			return nil
		}
		return fmt.Errorf("upload checkpoint %s failed: %v\n%s", point, err, output)
	}
	for _, id := range acceptance.StoreDataBoundaryScenarios() {
		t.Run(id+"/store-data-sqlite", func(t *testing.T) {
			if err := acceptance.CheckStoreDataBoundary(t.Context(), t.TempDir(), id, crash); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("storage-reference-edge-laundering/filesystem-store", func(t *testing.T) {
		if err := acceptance.CheckBlobUploadIsolation(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("storage-local-remote-cache-opt-in/store-data-sqlite", func(t *testing.T) {
		if err := acceptance.CheckBlobRemoteCacheEdges(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("storage-reachability-and-retention-tails/filesystem-store", checkStorageFilesystemCollection)
	t.Run("storage-reachability-and-retention-tails/s3-store-boundary", checkStorageS3Collection)
	t.Run("storage-reference-edge-laundering/filesystem-manifests", checkStorageFilesystemManifests)
	t.Run("storage-reference-edge-laundering/s3-manifests", checkStorageS3Manifests)
	t.Run("storage-reference-edge-laundering/filesystem-manifest-records", checkStorageFilesystemManifestRecords)
	t.Run("storage-reference-edge-laundering/s3-manifest-records", checkStorageS3ManifestRecords)
	t.Run("storage-components/dependency-key-contract", checkStorageDependencyKeys)
	t.Run("storage-components/dependency-key-containment", checkStorageKeyContainment)
	t.Run("storage-components/go-modules-resolver", checkStorageGoModulesResolver)
	pendingScenarios(t, "storage")
}
