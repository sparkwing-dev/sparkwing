package s3

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

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

// SDKMaxAttempts caps the AWS SDK's own retryer, which otherwise
// decides on its own how many times one call re-sends. Counting the
// first try, a failing request leaves at most this many billed requests
// behind.
const SDKMaxAttempts = 4

// SDKMaxBackoff ceilings the SDK's wait between those attempts, so a
// slow failure cannot stretch one call past a caller's patience.
const SDKMaxBackoff = 5 * time.Second

// ParseURL splits an s3://bucket/prefix URL into its bucket and its prefix
// with no leading or trailing slash.
func ParseURL(raw string) (bucket, prefix string, err error) {
	if !strings.HasPrefix(raw, "s3://") {
		return "", "", fmt.Errorf("s3: want an s3:// URL, got %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("s3: parse url: %w", err)
	}
	if u.Host == "" {
		return "", "", errors.New("s3: s3:// requires a bucket")
	}
	prefix = strings.TrimPrefix(u.Path, "/")
	prefix = strings.TrimSuffix(prefix, "/")
	return u.Host, prefix, nil
}

// NewClient builds an S3 client the one way this repository builds them:
// region and credentials from the AWS default chain (IRSA on EKS), the SDK
// retryer capped at [SDKMaxAttempts], and $SPARKWING_S3_ENDPOINT, when set,
// as a path-style endpoint for an S3-compatible store. optFns follow those
// options, so a caller adds a request budget here.
//
// safety: the only S3 client this repository constructs, so the SDK retryer is
// capped in exactly one place.
func NewClient(ctx context.Context, optFns ...func(*awss3.Options)) (*awss3.Client, error) {
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
	return awss3.NewFromConfig(cfg, append(opts, optFns...)...), nil
}

// Open parses an s3://bucket/prefix URL and returns a [NewClient] client
// whose every attempt is spent against the process-wide request budget. A
// service that keeps its own layout inside the bucket uses it instead of a
// whole artifact store.
func Open(ctx context.Context, raw string) (client *awss3.Client, bucket, prefix string, err error) {
	bucket, prefix, err = ParseURL(raw)
	if err != nil {
		return nil, "", "", err
	}
	client, err = NewClient(ctx, objectguard.WithBudget(objectguard.Shared()))
	if err != nil {
		return nil, "", "", err
	}
	return client, bucket, prefix, nil
}
