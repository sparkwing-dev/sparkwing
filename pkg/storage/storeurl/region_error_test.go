package storeurl

import (
	"context"
	"strings"
	"testing"
)

// A runner holding static credentials and no ~/.aws/config has no
// region, and the SDK's own answer for that -- "resolve auth scheme:
// resolve endpoint: endpoint rule error, Invalid region" -- names
// neither the variable to set nor the tool that wanted it.
func TestNewS3Client_MissingRegionNamesTheRemedy(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	_, err := newS3Client(context.Background())
	if err == nil {
		t.Fatal("got nil, want an error naming the missing region")
	}
	for _, want := range []string{"AWS_REGION", "s3 backend", "SPARKWING_S3_ENDPOINT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "endpoint rule error") {
		t.Errorf("error should not be the SDK's internal phrasing: %q", err.Error())
	}
}

// A region present means the client builds, so the guard cannot reject
// a working configuration.
func TestNewS3Client_RegionPresentBuilds(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")

	if _, err := newS3Client(context.Background()); err != nil {
		t.Fatalf("newS3Client: %v", err)
	}
}
