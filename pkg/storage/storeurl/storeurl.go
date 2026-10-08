// Package storeurl parses storage backend URLs and constructs the
// matching ArtifactStore / LogStore.
//
// Supported schemes:
//
//	fs:///abs/path           pkg/storage/fs (filesystem)
//	s3://bucket/prefix       pkg/storage/s3 (any S3-compatible store)
//	http(s)://host           pkg/storage/sparkwingcache
//
// An http(s) store authenticates with the run's $SPARKWING_CACHE_GRANT, or
// else $SPARKWING_CACHE_TOKEN, the bearer the cache requires on its routes.
//
// S3 credentials + region come from the standard AWS credential
// chain. $SPARKWING_S3_ENDPOINT overrides BaseEndpoint (R2, MinIO, etc.).
package storeurl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
)

// OpenArtifactStore parses raw and returns the matching backend.
// ctx is used only for the AWS config load.
func OpenArtifactStore(ctx context.Context, raw string) (storage.ArtifactStore, error) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case "fs":
		path, err := fsPath(rest)
		if err != nil {
			return nil, err
		}
		return fs.NewArtifactStore(path)
	case "s3":
		bucket, prefix, err := s3store.ParseURL("s3://" + rest)
		if err != nil {
			return nil, err
		}
		client, err := newS3Client(ctx, true)
		if err != nil {
			return nil, err
		}
		return s3store.NewArtifactStore(bucket, prefix, client), nil
	case "http", "https":
		return sparkwingcache.New(raw, authwire.CacheBearerFromEnv(), nil), nil
	default:
		return nil, fmt.Errorf("storeurl: unsupported scheme %q in %q", scheme, raw)
	}
}

// OpenLogStore parses raw and returns the matching backend.
func OpenLogStore(ctx context.Context, raw string) (storage.LogStore, error) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case "fs":
		path, err := fsPath(rest)
		if err != nil {
			return nil, err
		}
		return fs.NewLogStore(path)
	case "s3":
		bucket, prefix, err := s3store.ParseURL("s3://" + rest)
		if err != nil {
			return nil, err
		}
		client, err := newS3Client(ctx, true)
		if err != nil {
			return nil, err
		}
		return s3store.NewLogStore(bucket, prefix, client), nil
	default:
		return nil, fmt.Errorf("storeurl: unsupported scheme %q in %q", scheme, raw)
	}
}

func splitScheme(raw string) (scheme, rest string, err error) {
	if raw == "" {
		return "", "", errors.New("storeurl: empty URL")
	}
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return "", "", fmt.Errorf("storeurl: missing scheme:// in %q", raw)
	}
	return raw[:idx], raw[idx+3:], nil
}

func fsPath(rest string) (string, error) {
	if rest == "" {
		return "", errors.New("storeurl: fs:// requires a path")
	}
	if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "~") {
		return "", fmt.Errorf("storeurl: fs path must be absolute, got %q", rest)
	}
	if strings.HasPrefix(rest, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		rest = home + rest[1:]
	}
	return rest, nil
}

// OpenMeasurementStore opens raw for measurement alone: reading the
// store's own total, never serving it. maxPages bounds how many
// listings one measurement spends; zero takes the backend's default.
//
// Its requests stay outside the process-wide request budget on purpose.
// Totalling a bucket costs one LIST per thousand keys, so a bucket of a
// million objects would spend twice the whole per-minute list budget in
// one pass and leave every other reader refused for the rest of the
// minute. The page cap and the caller's deadline bound it instead.
func OpenMeasurementStore(ctx context.Context, raw string, maxPages int) (storage.ArtifactStore, error) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return nil, err
	}
	if scheme != "s3" {
		return OpenArtifactStore(ctx, raw)
	}
	bucket, prefix, err := s3store.ParseURL("s3://" + rest)
	if err != nil {
		return nil, err
	}
	client, err := newS3Client(ctx, false)
	if err != nil {
		return nil, err
	}
	store := s3store.NewArtifactStore(bucket, prefix, client)
	store.MaxUsagePages = maxPages
	return store, nil
}

// safety: a budgeted caller's requests all pass the process-wide request budget.
func newS3Client(ctx context.Context, budgeted bool) (*awss3.Client, error) {
	if !budgeted {
		return s3store.NewClient(ctx)
	}
	return s3store.NewClient(ctx, objectguard.WithBudget(sharedLimiter()))
}

// hack: an indirection so a test can hand newS3Client a budget of its own
// instead of the process-wide one, which is built once and never rebuilt.
var sharedLimiter = objectguard.Shared
