package storeurl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/stdoutlogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ProfileLookup resolves a profile name (from profiles.yaml) to its
// controller URL and bearer token. The factory invokes it for
// type=controller specs; other types ignore it. Pass nil when no
// controller-typed spec can appear.
//
// Shape matches sparkwing.ProfileLookup so the orchestrator's existing
// callback in profileLookupCallback() can be reused as-is.
type ProfileLookup func(name string) (controllerURL, token string, err error)

// OpenArtifactStoreFromSpec constructs an ArtifactStore from a
// backends.Spec. The spec must already have passed pkg/backends
// validation (surface allow-list, required fields per type).
//
// Recognized but not yet implemented backend types return a clear
// error so callers surface a configuration problem instead of
// silently falling back.
func OpenArtifactStoreFromSpec(ctx context.Context, spec backends.Spec, lookup ProfileLookup) (storage.ArtifactStore, error) {
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
	case backends.TypeController:
		url, token, err := resolveControllerProfile("cache", spec.Controller, lookup)
		if err != nil {
			return nil, err
		}
		return sparkwingcache.New(url, token, nil), nil
	case backends.TypeGCS, backends.TypeAzureBlob:
		return nil, unimplemented("cache", spec.Type)
	default:
		return nil, fmt.Errorf("cache backend type %q is not recognized", spec.Type)
	}
}

// OpenLogStoreFromSpec constructs a LogStore from a backends.Spec.
// See OpenArtifactStoreFromSpec for error semantics.
func OpenLogStoreFromSpec(ctx context.Context, spec backends.Spec, lookup ProfileLookup) (storage.LogStore, error) {
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
	case backends.TypeStdout:
		if err := stdoutlogs.CheckSpec(spec.Bucket, spec.Prefix, spec.Path, spec.URL, spec.URLSource, spec.Token); err != nil {
			return nil, err
		}
		return stdoutlogs.New(), nil
	case backends.TypeController:
		url, token, err := resolveControllerProfile("logs", spec.Controller, lookup)
		if err != nil {
			return nil, err
		}
		return sparkwinglogs.New(url, nil, token), nil
	case backends.TypeGCS, backends.TypeAzureBlob:
		return nil, unimplemented("logs", spec.Type)
	default:
		return nil, fmt.Errorf("logs backend type %q is not recognized", spec.Type)
	}
}

// resolveControllerProfile validates the controller field and asks the
// lookup callback for the profile's URL and bearer token. Centralizes
// the "factory was handed a controller-typed spec but no lookup
// callback" guard so cache and logs surfaces give identical errors.
func resolveControllerProfile(surface, controller string, lookup ProfileLookup) (string, string, error) {
	if controller == "" {
		return "", "", fmt.Errorf("%s backend type=controller requires controller: <profile-name>", surface)
	}
	if lookup == nil {
		return "", "", fmt.Errorf("%s backend type=controller needs a profile lookup; caller did not provide one", surface)
	}
	url, token, err := lookup(controller)
	if err != nil {
		return "", "", fmt.Errorf("%s backend profile %q: %w", surface, controller, err)
	}
	if url == "" {
		return "", "", fmt.Errorf("%s backend profile %q resolved to an empty URL", surface, controller)
	}
	return url, token, nil
}

// OpenStateStoreFromSpec constructs a StateStore from a backends.Spec.
// See OpenArtifactStoreFromSpec for error semantics. The lookup
// callback is consulted for type=controller specs; pass nil when no
// controller-typed spec can appear.
//
// For type=sqlite, spec.Path is required and names the SQLite database
// file. Callers that want the historical default (~/.sparkwing/state.db)
// should pass that path explicitly so the factory has a single,
// caller-provided source of truth.
func OpenStateStoreFromSpec(ctx context.Context, spec backends.Spec, lookup ProfileLookup) (storage.StateStore, error) {
	switch spec.Type {
	case backends.TypeSQLite:
		path, err := expandPath(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("state sqlite: %w", err)
		}
		return store.Open(path)
	case backends.TypeS3:
		s3client, err := newS3Client(ctx)
		if err != nil {
			return nil, err
		}
		art := s3store.NewArtifactStore(spec.Bucket, spec.Prefix, s3client)
		return s3state.New(art), nil
	case backends.TypeController:
		url, token, err := resolveControllerProfile("state", spec.Controller, lookup)
		if err != nil {
			return nil, err
		}
		return client.NewWithToken(url, nil, token), nil
	case backends.TypeGCS, backends.TypeAzureBlob:
		return nil, unimplemented("state", spec.Type)
	case backends.TypePostgres, backends.TypeMySQL:
		return nil, unimplemented("state", spec.Type)
	default:
		return nil, fmt.Errorf("state backend type %q is not recognized", spec.Type)
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
