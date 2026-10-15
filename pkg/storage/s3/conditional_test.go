package s3

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type createOnlyAPI struct {
	API
	put func(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
}

func (a createOnlyAPI) PutObject(_ context.Context, q *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return a.put(q)
}

func TestPutIfAbsent(t *testing.T) {
	transportErr := errors.New("transport failed")
	for _, fault := range []string{"", "PreconditionFailed", "ConditionalRequestConflict", "412", "409", "transport"} {
		t.Run(fault, func(t *testing.T) {
			api := createOnlyAPI{put: func(q *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
				body, err := io.ReadAll(q.Body)
				if err != nil {
					t.Fatal(err)
				}
				if aws.ToString(q.Bucket) != "bucket" || aws.ToString(q.Key) != "logs/key" || aws.ToString(q.IfNoneMatch) != "*" || q.IfMatch != nil || string(body) != "bytes" {
					t.Fatalf("unbound create: %+v, %q", q, body)
				}
				if fault == "transport" {
					return nil, transportErr
				}
				if fault != "" {
					return nil, &smithy.GenericAPIError{Code: fault}
				}
				return &s3.PutObjectOutput{}, nil
			}}
			err := NewArtifactStore("bucket", "logs", api).PutIfAbsent(t.Context(), "key", strings.NewReader("bytes"))
			switch fault {
			case "":
				if err != nil {
					t.Fatal(err)
				}
			case "transport":
				if !errors.Is(err, transportErr) {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, storage.ErrPreconditionFailed) {
					t.Fatal(err)
				}
			}
		})
	}
}
