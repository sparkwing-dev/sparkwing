package storeurl

import (
	"context"
	"strings"
	"testing"
)

func TestMeasurementStoreMissingRegionNamesTheRemedy(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	cfg, err := ParseMeasurementURL("s3://bucket/prefix")
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.Open(context.Background(), 0)
	if err == nil {
		t.Fatal("got nil, want an error naming the missing region")
	}
	for _, want := range []string{"AWS_REGION", "s3 backend", "AWS_ENDPOINT_URL_S3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "endpoint rule error") {
		t.Errorf("error should not be the SDK's internal phrasing: %q", err.Error())
	}
}

func TestMeasurementStoreRegionPresentBuilds(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")

	cfg, err := ParseMeasurementURL("s3://bucket/prefix")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Open(context.Background(), 0); err != nil {
		t.Fatalf("OpenMeasurementStore: %v", err)
	}
}
