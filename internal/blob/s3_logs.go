package blob

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// SignLogPut signs a create-only PUT to one server-derived log key. The caller
// must check writer authority and retention readiness first. Future slots have
// no known payload size or digest. Reads and commits must verify the stored bytes.
// S3 rejects every overwrite, including identical retries, with HTTP 412.
func (s *S3) SignLogPut(ctx context.Context, key string, expiry time.Time) (string, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if _, err := logObjectLimit(key); err != nil {
		return "", nil, err
	}
	lifetime := expiry.Sub(time.Now())
	if lifetime <= 0 || lifetime > limits.LogUploadURLTTL {
		return "", nil, fmt.Errorf("invalid log upload expiry")
	}
	put, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), IfNoneMatch: aws.String("*"),
	}, func(o *s3.PresignOptions) { o.Expires = lifetime })
	if err != nil {
		return "", nil, err
	}
	return put.URL, signedHeaders(put.SignedHeader), nil
}
