package blob

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// SignLogPut signs a create-only PUT of exact bytes to one server-derived log
// key. The caller must check writer authority and retention readiness first.
// S3 rejects every overwrite, including identical retries, with HTTP 412.
func (s *S3) SignLogPut(ctx context.Context, key string, size int64, digest Digest, expiry time.Time) (string, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err := validLogPayload(key, size, digest); err != nil {
		return "", nil, err
	}
	lifetime := time.Until(expiry)
	if lifetime <= 0 || lifetime > limits.LogUploadURLTTL {
		return "", nil, fmt.Errorf("invalid log upload expiry")
	}
	raw, err := hex.DecodeString(string(digest))
	if err != nil {
		return "", nil, err
	}
	put, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.PhysicalKey(key)), IfNoneMatch: aws.String("*"),
		ContentLength: aws.Int64(size), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw)),
	}, func(o *s3.PresignOptions) { o.Expires = lifetime })
	if err != nil {
		return "", nil, err
	}
	return put.URL, signedHeaders(put.SignedHeader), nil
}
