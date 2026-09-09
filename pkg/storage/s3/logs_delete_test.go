package s3

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"strings"
	"testing"
)

type deniedDeleteAPI struct{ API }

func (s deniedDeleteAPI) DeleteObjects(_ context.Context, in *awss3.DeleteObjectsInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectsOutput, error) {
	return &awss3.DeleteObjectsOutput{Errors: []s3types.Error{{Key: in.Delete.Objects[0].Key, Code: aws.String("AccessDenied"), Message: aws.String("retained")}}}, nil
}

func TestDeleteRunReportsObjectFailures(t *testing.T) {
	client, close := fakeS3(t)
	defer close()
	logs := NewLogStore(testBucket, "logs", deniedDeleteAPI{client})
	if err := logs.Append(t.Context(), "run-1", "build", []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	err := logs.DeleteRun(t.Context(), "run-1")
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "logs/run-1/build/") {
		t.Fatalf("DeleteRun = %v, want denied key", err)
	}
}
