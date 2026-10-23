package blob

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// LogClassReady checks that the bucket expires every object in the requested
// class. Use it as the batch signer's lifecycle check. It never changes bucket
// rules, which may also govern unrelated operator-owned prefixes.
func (s *S3) LogClassReady(ctx context.Context, class string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	days, ok := LogClassDays(class)
	if !ok {
		return fmt.Errorf("invalid log retention class %q", class)
	}
	out, err := s.client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(s.bucket)})
	if err != nil && !s3Error(err, "NoSuchLifecycleConfiguration") {
		return fmt.Errorf("read log lifecycle: %w", err)
	}
	prefix := s.PhysicalKey(logClassPrefix(class))
	if err == nil && logLifecycleExpires(out.Rules, prefix, days) {
		return nil
	}
	return fmt.Errorf("bucket %s has no enabled lifecycle rule expiring prefix %q after %d days", s.bucket, prefix, days)
}

// logLifecycleExpires checks an already-fetched lifecycle configuration. A pure
// prefix rule must cover the whole class. Tag and size filters leave some log
// objects without expiry and cannot establish readiness.
func logLifecycleExpires(rules []types.LifecycleRule, prefix string, days int) bool {
	if prefix == "" || days < 1 {
		return false
	}
	for _, r := range rules {
		if r.Status != types.ExpirationStatusEnabled || r.Expiration == nil || int64(aws.ToInt32(r.Expiration.Days)) != int64(days) {
			continue
		}
		rulePrefix := aws.ToString(r.Prefix) //nolint:staticcheck // the deprecated element still selects objects on rules written with it.
		if f := r.Filter; f != nil {
			if f.Tag != nil || f.And != nil || f.ObjectSizeGreaterThan != nil || f.ObjectSizeLessThan != nil {
				continue
			}
			rulePrefix = aws.ToString(f.Prefix)
		}
		if strings.HasPrefix(prefix, rulePrefix) {
			return true
		}
	}
	return false
}
