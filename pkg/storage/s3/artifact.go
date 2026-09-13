// Package s3 implements storage.ArtifactStore + storage.LogStore on
// top of any S3-compatible object store. Both surfaces run the
// shared pkg/storage/conformance suites against an in-memory
// gofakes3 server.
//
// Object layout (Prefix is caller-supplied):
//
//	<Prefix>/<key>                            ArtifactStore
//	<Prefix>/<runID>/<nodeID>/<seq>.ndjson    LogStore (rolling)
//
// Logs use object-per-Append because S3 has no append primitive;
// read-modify-write would lose lines on concurrent appenders. A
// nanosecond timestamp + monotonic seq keeps Append keys lex-sortable
// across goroutines and processes.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// API is the subset of the s3 client used by both stores; declared
// as an interface so tests can substitute a fake.
type API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// ArtifactStore implements storage.ArtifactStore over an S3-compatible
// object store using a single PUT per upload.
type ArtifactStore struct {
	Bucket string
	Prefix string // optional namespace within bucket; "" means bucket root
	Client API

	// MaxUsagePages bounds how many listings one [ArtifactStore.Usage]
	// spends. Zero takes DefaultMaxUsagePages.
	MaxUsagePages int

	casOnce casProbe
}

// NewArtifactStore wires an ArtifactStore around the provided client.
func NewArtifactStore(bucket, prefix string, client API) *ArtifactStore {
	return &ArtifactStore{
		Bucket: bucket,
		Prefix: strings.TrimSuffix(prefix, "/"),
		Client: client,
	}
}

var (
	_ storage.ArtifactStore = (*ArtifactStore)(nil)
	_ storage.UsageReporter = (*ArtifactStore)(nil)
)

func (s *ArtifactStore) artifactKey(key string) string {
	if s.Prefix == "" {
		return key
	}
	return s.Prefix + "/" + key
}

func (s *ArtifactStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s.artifactKey(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	return out.Body, nil
}

func (s *ArtifactStore) Put(ctx context.Context, key string, r io.Reader) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s.artifactKey(key)),
		Body:   r,
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (s *ArtifactStore) Has(ctx context.Context, key string) (bool, error) {
	_, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s.artifactKey(key)),
	})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("s3 head %s: %w", key, err)
}

func (s *ArtifactStore) Delete(ctx context.Context, key string) error {
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s.artifactKey(key)),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

// List returns every key under prefix, with the configured Prefix
// stripped so callers see the same keyspace they Put under.
func (s *ArtifactStore) List(ctx context.Context, prefix string) ([]string, error) {
	full := s.artifactKey(prefix)
	var token *string
	var out []string
	stripPrefix := ""
	if s.Prefix != "" {
		stripPrefix = s.Prefix + "/"
	}
	for {
		page, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.Bucket),
			Prefix:            aws.String(full),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			k := *obj.Key
			if stripPrefix != "" {
				k = strings.TrimPrefix(k, stripPrefix)
			}
			out = append(out, k)
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
}

// DefaultMaxUsagePages bounds one measurement at a thousand listings,
// which covers a million objects at the thousand-key page S3 returns.
// A store larger than that reports a partial total rather than paging
// on until its caller's deadline.
const DefaultMaxUsagePages = 1000

// Usage totals the objects under the store's prefix with one paginated
// listing. A bucket of a million objects costs a thousand LIST
// requests, so callers measure on a schedule rather than per write.
//
// It stops and reports the total as partial when it reaches
// MaxUsagePages, or when ctx is done, so a bucket too large to walk
// bounds the walk rather than the other way round.
func (s *ArtifactStore) Usage(ctx context.Context) (storage.StoreUsage, error) {
	maxPages := s.MaxUsagePages
	if maxPages <= 0 {
		maxPages = DefaultMaxUsagePages
	}
	var token *string
	var usage storage.StoreUsage
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			usage.Partial = true
			break
		}
		if err := ctx.Err(); err != nil {
			usage.Partial = true
			break
		}
		page, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.Bucket),
			Prefix:            aws.String(s.Prefix),
			ContinuationToken: token,
		})
		if err != nil {
			// safety: a deadline that fired mid-listing is a partial measurement, not
			// a broken bucket, and the caller distinguishes the two on Partial.
			if ctx.Err() != nil {
				usage.Partial = true
				break
			}
			return storage.StoreUsage{}, fmt.Errorf("s3 usage %s: %w", s.Bucket, err)
		}
		for _, obj := range page.Contents {
			usage.Objects++
			if obj.Size != nil {
				usage.Bytes += *obj.Size
			}
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	usage.ObservedAt = time.Now().UTC()
	return usage, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
