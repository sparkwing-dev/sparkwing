package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func r2LogTestConfigured(endpoint, bucket, profile string) (bool, error) {
	if endpoint == "" && bucket == "" {
		return false, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".r2.cloudflarestorage.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() != "" {
		return false, errors.New("SPARKWING_R2_TEST_ENDPOINT must be an HTTPS R2 account endpoint")
	}
	testName := func(s string) bool { return strings.Contains(s, "test") || strings.Contains(s, "scratch") }
	if bucket == "" || !testName(bucket) || strings.ContainsAny(bucket, "/\\: ") || profile == "" || !testName(profile) {
		return false, errors.New("set SPARKWING_R2_TEST_BUCKET and AWS_PROFILE to existing designated test/scratch resources")
	}
	return true, nil
}

func TestR2LogTestConfiguration(t *testing.T) {
	endpoint := "https://account.r2.cloudflarestorage.com"
	for _, c := range []struct {
		name, endpoint, bucket, profile string
		ready, bad                      bool
	}{
		{name: "unconfigured"},
		{name: "ambient profile only", profile: "production"},
		{"test", endpoint, "sparkwing-test", "r2-test", true, false},
		{"scratch", endpoint, "sparkwing-scratch", "r2-test", true, false},
		{"missing bucket", endpoint, "", "r2-test", false, true},
		{"missing endpoint", "", "sparkwing-test", "r2-test", false, true},
		{"production bucket", endpoint, "sparkwing-production", "r2-test", false, true},
		{"ambient credentials", endpoint, "sparkwing-test", "", false, true},
		{"production profile", endpoint, "sparkwing-test", "production", false, true},
		{"foreign endpoint", "https://objects.example", "sparkwing-test", "r2-test", false, true},
		{"insecure endpoint", "http://account.r2.cloudflarestorage.com", "sparkwing-test", "r2-test", false, true},
		{"URL credentials", "https://secret@account.r2.cloudflarestorage.com", "sparkwing-test", "r2-test", false, true},
		{"URL query", endpoint + "?secret=value", "sparkwing-test", "r2-test", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ready, err := r2LogTestConfigured(c.endpoint, c.bucket, c.profile)
			if ready != c.ready || (err != nil) != c.bad {
				t.Fatalf("configured=%v error=%v", ready, err)
			}
		})
	}
}

func TestR2LogSigningRejectsChangedBytes(t *testing.T) {
	endpoint, bucket, profile := os.Getenv("SPARKWING_R2_TEST_ENDPOINT"), os.Getenv("SPARKWING_R2_TEST_BUCKET"), os.Getenv("AWS_PROFILE")
	ready, err := r2LogTestConfigured(endpoint, bucket, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Skip("R2 proof pending: configure an existing test/scratch bucket, endpoint and AWS_PROFILE")
	}
	cfg, err := config.LoadDefaultConfig(t.Context(), config.WithRegion("auto"), config.WithSharedConfigProfile(profile))
	if err != nil {
		t.Fatalf("load test profile: %T", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
		o.RetryMaxAttempts = 1
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	})
	prefix := "log-signing-proof/" + strings.ToLower(rand.Text()) + "/"
	t.Logf("test bucket=%s prefix=%s", bucket, prefix)
	backend, err := NewS3(client, bucket, prefix)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	// safety: delete only this invocation's exact keys, even after cancellation.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		for _, key := range keys {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
				t.Errorf("cleanup DELETE failed (%T)", err)
				continue
			}
			if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); !s3Error(err, "NotFound", "NoSuchKey") {
				t.Errorf("cleanup HEAD did not prove absence (%T)", err)
			}
		}
		list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(1)})
		if err != nil {
			t.Errorf("cleanup LIST failed (%T)", err)
			return
		}
		if len(list.Contents) != 0 {
			t.Error("test prefix is not empty after cleanup")
			return
		}
		t.Logf("cleanup verified: %d exact keys absent; prefix %s empty", len(keys), prefix)
	})
	httpClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	put := func(t *testing.T, raw string, headers map[string]string, body []byte) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, raw, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("construct PUT (%T)", err)
		}
		for k, v := range headers {
			if !strings.EqualFold(k, "Content-Length") {
				req.Header.Set(k, v)
			}
		}
		// safety: actual wire framing must match the body so wrong-size refusal
		// comes from R2, not net/http's local ContentLength check.
		req.ContentLength = int64(len(body))
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(newTransferFailure(http.MethodPut, raw, err))
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 8192))
		if err = errors.Join(err, response.Body.Close()); err != nil {
			t.Fatalf("read PUT response (%T)", err)
		}
		var failure struct{ Code string }
		if len(data) > 0 && xml.Unmarshal(data, &failure) != nil {
			t.Fatal("PUT response is not XML")
		}
		t.Logf("PUT status=%d code=%s request_id=%s", response.StatusCode, failure.Code, response.Header.Get("X-Amz-Request-Id"))
		return response.StatusCode, failure.Code
	}
	original := []byte("bounded log payload\n")
	for _, name := range []string{"seg-000001.ndjson.gz", "manifest.json"} {
		for _, change := range []string{"valid", "larger", "smaller", "corrupt", "checksum header", "missing checksum", "missing create-only"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				key := fmt.Sprintf("logs/r30/teams/test/runs/run-%d/units/plan/g1/%s", len(keys)+1, name)
				physical := backend.PhysicalKey(key)
				keys = append(keys, physical)
				raw, headers, err := backend.SignLogPut(t.Context(), key, int64(len(original)), digest(string(original)), time.Now().Add(2*time.Minute))
				if err != nil {
					t.Fatalf("sign PUT (%T)", err)
				}
				body, changed := bytes.Clone(original), maps.Clone(headers)
				switch change {
				case "larger":
					body = append(body, 'x')
				case "smaller":
					body = body[:len(body)-1]
				case "corrupt":
					body[0] ^= 1
				case "checksum header":
					changed["X-Amz-Checksum-Sha256"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
				case "missing checksum":
					delete(changed, "X-Amz-Checksum-Sha256")
				case "missing create-only":
					delete(changed, "If-None-Match")
				}
				status, code := put(t, raw, changed, body)
				if change != "valid" {
					if status < 400 || status >= 500 {
						t.Fatalf("changed PUT status=%d code=%s, want storage refusal", status, code)
					}
					if change == "corrupt" && (status != http.StatusBadRequest || code != "BadDigest") {
						t.Fatalf("body checksum refusal=%d %s, want 400 BadDigest", status, code)
					}
					if _, err := client.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(physical)}); !s3Error(err, "NotFound", "NoSuchKey") {
						t.Fatalf("rejected PUT left an object or absence is unproven (%T)", err)
					}
					status, code = put(t, raw, headers, original)
				}
				if status < 200 || status >= 300 {
					t.Fatalf("valid PUT status=%d code=%s", status, code)
				}
				if status, code = put(t, raw, headers, original); status != http.StatusPreconditionFailed {
					t.Fatalf("overwrite=%d %s, want 412", status, code)
				}
				stored, err := backend.Get(t.Context(), key)
				if err != nil {
					t.Fatalf("read valid object (%T)", err)
				}
				actual, err := io.ReadAll(io.LimitReader(stored, int64(len(original)+1)))
				if err = errors.Join(err, stored.Close()); err != nil || !bytes.Equal(actual, original) {
					t.Fatalf("stored bytes differ: read error type=%T", err)
				}
			})
		}
	}
}
