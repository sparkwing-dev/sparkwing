package controller

import (
	"bytes"
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// The bucket's own class directories name a class no configuration or
// record names any more, so a team's purge still reaches it.
func TestS3LogObjectsListTheClassesTheBucketHolds(t *testing.T) {
	fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(fake.Close)
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(fake.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	ctx := context.Background()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("logs-bucket")}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cache/logs/r30/teams/a/x", "cache/logs/r365/teams/a/y", "cache/other/z"} {
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("logs-bucket"), Key: aws.String(key), Body: bytes.NewReader(nil)}); err != nil {
			t.Fatal(err)
		}
	}
	objs, ok := New(nil, nil).WithDirectUploads(client, "logs-bucket", "cache").logObjects().(s3LogObjects)
	if !ok {
		t.Fatal("no S3 log store")
	}
	got, err := objs.classes(ctx)
	slices.Sort(got)
	if err != nil || !slices.Equal(got, []string{"r30", "r365"}) {
		t.Fatalf("classes = %v, %v; want r30 and r365", got, err)
	}
}
