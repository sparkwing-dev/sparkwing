// Package storeurl parses storage backend URLs and constructs the
// matching ArtifactStore / LogStore.
//
// Supported schemes:
//
//	fs:///abs/path           pkg/storage/fs (filesystem)
//	s3://bucket/prefix       pkg/storage/s3 (any S3-compatible store)
//	http(s)://host           pkg/storage/sparkwingcache
//
// An http(s) store authenticates with $SPARKWING_CACHE_TOKEN, the bearer
// the cache requires on its /bin/ routes.
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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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
		client, err := newS3Client(ctx)
		if err != nil {
			return nil, err
		}
		return s3store.NewArtifactStore(bucket, prefix, client), nil
	case "http", "https":
		return sparkwingcache.New(raw, bincache.CacheToken(), nil), nil
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
		client, err := newS3Client(ctx)
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

func newS3Client(ctx context.Context) (*awss3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
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
	return awss3.NewFromConfig(cfg, opts...), nil
}
