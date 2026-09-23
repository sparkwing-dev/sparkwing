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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
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
		bucket, prefix, err := s3BucketPrefix(rest)
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
		bucket, prefix, err := s3BucketPrefix(rest)
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

func s3BucketPrefix(rest string) (bucket, prefix string, err error) {
	u, err := url.Parse("s3://" + rest)
	if err != nil {
		return "", "", fmt.Errorf("storeurl: parse s3 url: %w", err)
	}
	if u.Host == "" {
		return "", "", errors.New("storeurl: s3:// requires a bucket")
	}
	prefix = strings.TrimPrefix(u.Path, "/")
	prefix = strings.TrimSuffix(prefix, "/")
	return u.Host, prefix, nil
}

// SDKMaxAttempts caps the AWS SDK's own retryer, which otherwise
// decides on its own how many times one call re-sends. Counting the
// first try, a failing request leaves at most this many billed requests
// behind.
const SDKMaxAttempts = 4

// SDKMaxBackoff ceilings the SDK's wait between those attempts, so a
// slow failure cannot stretch one call past a caller's patience.
const SDKMaxBackoff = 5 * time.Second

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
	bucket, prefix, err := s3BucketPrefix(rest)
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

// safety: the only S3 client this repository constructs, so a budgeted caller's
// requests all pass the process-wide request budget and the SDK retryer is
// capped in exactly one place.
func newS3Client(ctx context.Context, budgeted bool) (*awss3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = SDKMaxAttempts
			o.MaxBackoff = SDKMaxBackoff
		})
	}))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New(
			"no AWS region configured, which an s3 backend needs: set AWS_REGION " +
				"(or a region in ~/.aws/config). For a non-AWS S3-compatible store, " +
				"set SPARKWING_S3_ENDPOINT as well and any region value will do")
	}
	opts := []func(*awss3.Options){}
	if ep := os.Getenv("SPARKWING_S3_ENDPOINT"); ep != "" {
		opts = append(opts, func(o *awss3.Options) {
			o.BaseEndpoint = aws.String(ep)
			o.UsePathStyle = true
		})
	}
	if budgeted {
		limiter, lerr := sharedLimiter()
		if lerr != nil {
			return nil, fmt.Errorf("object-store request budget: %w", lerr)
		}
		opts = append(opts, objectguard.WithBudget(limiter))
	}
	return awss3.NewFromConfig(cfg, opts...), nil
}

// hack: an indirection so a test can hand newS3Client a budget of its own
// instead of the process-wide one, which is built once and never rebuilt.
var sharedLimiter = objectguard.Shared

// OpenS3 parses an s3://bucket/prefix URL and returns a client built
// the one way this repository builds them: region and credentials from
// the AWS default chain (IRSA on EKS), the SDK retryer capped at
// [SDKMaxAttempts], and every attempt spent against the process-wide
// request budget. A service that keeps its own layout inside the bucket
// uses it instead of [OpenArtifactStore].
func OpenS3(ctx context.Context, raw string) (client *awss3.Client, bucket, prefix string, err error) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return nil, "", "", err
	}
	if scheme != "s3" {
		return nil, "", "", fmt.Errorf("storeurl: want an s3:// URL, got %q", raw)
	}
	bucket, prefix, err = s3BucketPrefix(rest)
	if err != nil {
		return nil, "", "", err
	}
	client, err = newS3Client(ctx, true)
	if err != nil {
		return nil, "", "", err
	}
	return client, bucket, prefix, nil
}
