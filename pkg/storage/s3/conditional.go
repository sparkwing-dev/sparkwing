package s3

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// PutIfAbsent stores a new object with S3's create-only precondition.
func (s *ArtifactStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(s.artifactKey(key)),
		Body: r, IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "PreconditionFailed", "ConditionalRequestConflict", "412", "409":
				return storage.ErrPreconditionFailed
			}
		}
		return fmt.Errorf("s3 put-if-absent %s: %w", key, err)
	}
	return nil
}
