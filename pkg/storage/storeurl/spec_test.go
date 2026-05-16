package storeurl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func TestOpenArtifactStoreFromSpec_Filesystem(t *testing.T) {
	dir := t.TempDir()
	store, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestOpenLogStoreFromSpec_Filesystem(t *testing.T) {
	dir := t.TempDir()
	store, err := storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestOpenArtifactStoreFromSpec_Unimplemented(t *testing.T) {
	cases := []string{backends.TypeGCS, backends.TypeAzureBlob, backends.TypeController}
	for _, ty := range cases {
		t.Run(ty, func(t *testing.T) {
			_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
				backends.Spec{Type: ty, Bucket: "x"})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "not implemented in this build") {
				t.Errorf("want 'not implemented in this build', got: %v", err)
			}
		})
	}
}

func TestOpenLogStoreFromSpec_Unimplemented(t *testing.T) {
	for _, ty := range []string{backends.TypeGCS, backends.TypeAzureBlob, backends.TypeController, backends.TypeStdout} {
		t.Run(ty, func(t *testing.T) {
			_, err := storeurl.OpenLogStoreFromSpec(context.Background(),
				backends.Spec{Type: ty, Bucket: "x"})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "not implemented in this build") {
				t.Errorf("want 'not implemented in this build', got: %v", err)
			}
		})
	}
}

func TestOpenFromSpec_UnrecognizedType(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Errorf("want 'not recognized', got: %v", err)
	}
	_, err = storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Errorf("want 'not recognized', got: %v", err)
	}
}

func TestOpenArtifactStoreFromSpec_FilesystemMissingPath(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem})
	if err == nil {
		t.Fatal("expected error")
	}
	// not validated again here; pkg/backends.Validate catches it.
	// The factory still surfaces it through expandPath.
	if !strings.Contains(err.Error(), "path is required") {
		t.Errorf("want 'path is required', got: %v", err)
	}
}

// sanity: errors.New baseline so import is non-trivial in case the
// test file is trimmed in the future.
var _ = errors.New
