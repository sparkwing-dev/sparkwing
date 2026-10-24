package storeurl

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	s3store "github.com/sparkwing-dev/sparkwing/internal/storage/s3"
)

type measurementHTTP func(*http.Request) (*http.Response, error)

func (f measurementHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func s3Env(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	t.Setenv("AWS_ENDPOINT_URL_S3", "https://objects.invalid")
}

func measurementWithHTTP(t *testing.T, maxPages int, transport measurementHTTP) storage.UsageReporter {
	t.Helper()
	cfg, err := ParseMeasurementURL("s3://bucket/prefix")
	if err != nil {
		t.Fatal(err)
	}
	reporter, err := cfg.Open(t.Context(), maxPages)
	if err != nil {
		t.Fatal(err)
	}
	store := reporter.(*s3store.MeasurementStore)
	options := store.Client.(*awss3.Client).Options()
	options.HTTPClient = transport
	store.Client = awss3.New(options)
	return reporter
}

func TestMeasurementStoreStaysOutsideTheListBudget(t *testing.T) {
	s3Env(t)
	hits := 0
	transport := measurementHTTP(func(r *http.Request) (*http.Response, error) {
		hits++
		if r.Method != http.MethodGet || r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "prefix/" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>prefix/object</Key><Size>7</Size></Contents></ListBucketResult>`)), Request: r}, nil
	})
	spent := objectguard.New(objectguard.Config{Enabled: true, Limits: map[objectguard.Class]objectguard.Limit{objectguard.ClassList: {PerMinute: 1}}})
	if err := spent.Allow(objectguard.ClassList); err != nil {
		t.Fatal(err)
	}
	budgeted, err := s3store.NewClient(t.Context(), objectguard.WithBudget(spent), func(o *awss3.Options) { o.HTTPClient = transport })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budgeted.ListObjectsV2(t.Context(), &awss3.ListObjectsV2Input{Bucket: aws.String("bucket")}); !errors.Is(err, objectguard.ErrBudgetExceeded) || hits != 0 {
		t.Fatalf("spent budget reached bucket: %d requests, %v", hits, err)
	}
	reporter := measurementWithHTTP(t, 1, transport)
	usage, err := reporter.Usage(t.Context())
	if err != nil || usage.Bytes != 7 || usage.Objects != 1 || usage.Partial || hits != 1 {
		t.Fatalf("measurement = %+v, %d requests, %v", usage, hits, err)
	}
}

func TestMeasurementStoreRetainsThePageCap(t *testing.T) {
	s3Env(t)
	hits := 0
	reporter := measurementWithHTTP(t, 2, func(r *http.Request) (*http.Response, error) {
		hits++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>prefix/object</Key><Size>7</Size></Contents></ListBucketResult>`)), Request: r}, nil
	})
	usage, err := reporter.Usage(t.Context())
	if err != nil || usage.Bytes != 14 || usage.Objects != 2 || !usage.Partial || hits != 2 {
		t.Fatalf("bounded measurement = %+v, %d requests, %v", usage, hits, err)
	}
}

func TestMeasurementStoreCapsSDKRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s3Env(t)
		hits := 0
		reporter := measurementWithHTTP(t, 1, func(r *http.Request) (*http.Response, error) {
			hits++
			return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<Error><Code>InternalError</Code><Message>bucket unavailable</Message></Error>`)), Request: r}, nil
		})
		if _, err := reporter.Usage(t.Context()); err == nil || hits != s3store.SDKMaxAttempts {
			t.Fatalf("failed listing: %d requests, %v", hits, err)
		}
	})
}
