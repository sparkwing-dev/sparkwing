// Package s3 measures physical bucket usage and configures S3 clients.
package s3

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
)

// API lists objects for physical usage measurement.
type API interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// MeasurementStore reports bounded physical byte and object totals.
type MeasurementStore struct {
	Bucket        string
	Prefix        string
	Client        API
	MaxUsagePages int
}

// NewMeasurementStore binds bucket measurement to one namespace.
func NewMeasurementStore(bucket, prefix string, client API) *MeasurementStore {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &MeasurementStore{Bucket: bucket, Prefix: prefix, Client: client}
}

var _ storage.UsageReporter = (*MeasurementStore)(nil)

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
func (s *MeasurementStore) Usage(ctx context.Context) (storage.StoreUsage, error) {
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
