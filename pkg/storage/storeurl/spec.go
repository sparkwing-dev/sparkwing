package storeurl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

// OpenArtifactStoreFromSpec constructs an ArtifactStore from a
// backends.Spec. The spec must already have passed pkg/backends
// validation (surface allow-list, required fields per type).
//
// Recognized but not yet implemented backend types return a clear
// error so callers surface a configuration problem instead of
// silently falling back.
func OpenArtifactStoreFromSpec(ctx context.Context, spec backends.Spec) (storage.ArtifactStore, error) {
	switch spec.Type {
	case backends.TypeFilesystem:
		path, err := expandPath(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("cache filesystem: %w", err)
		}
		return fs.NewArtifactStore(path)
	case backends.TypeS3:
		client, err := newS3Client(ctx)
		if err != nil {
			return nil, err
		}
		return s3store.NewArtifactStore(spec.Bucket, spec.Prefix, client), nil
	case backends.TypeGCS, backends.TypeAzureBlob, backends.TypeController:
		return nil, unimplemented("cache", spec.Type)
	default:
		return nil, fmt.Errorf("cache backend type %q is not recognized", spec.Type)
	}
}

// OpenLogStoreFromSpec constructs a LogStore from a backends.Spec.
// See OpenArtifactStoreFromSpec for error semantics.
func OpenLogStoreFromSpec(ctx context.Context, spec backends.Spec) (storage.LogStore, error) {
	switch spec.Type {
	case backends.TypeFilesystem:
		path, err := expandPath(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("logs filesystem: %w", err)
		}
		return fs.NewLogStore(path)
	case backends.TypeS3:
		client, err := newS3Client(ctx)
		if err != nil {
			return nil, err
		}
		return s3store.NewLogStore(spec.Bucket, spec.Prefix, client), nil
	case backends.TypeGCS, backends.TypeAzureBlob, backends.TypeController, backends.TypeStdout:
		return nil, unimplemented("logs", spec.Type)
	default:
		return nil, fmt.Errorf("logs backend type %q is not recognized", spec.Type)
	}
}

func unimplemented(surface, t string) error {
	return fmt.Errorf("%s backend type %q is recognized but not implemented in this build", surface, t)
}

func expandPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return home + p[1:], nil
	}
	return p, nil
}
