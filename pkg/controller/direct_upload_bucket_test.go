package controller_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

const directUploadBucketName = "sparkwing-data"

func directUploadBucket(t *testing.T) *s3.Client {
	t.Helper()
	written := gofakes3.FixedTimeSource(time.Now())
	fake := httptest.NewServer(gofakes3.New(s3mem.New(s3mem.WithTimeSource(written)),
		gofakes3.WithTimeSource(written), gofakes3.WithTimeSkewLimit(0)).Server())
	t.Cleanup(fake.Close)
	raw := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(fake.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	if _, err := raw.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(directUploadBucketName)}); err != nil {
		t.Fatal(err)
	}
	return raw
}
