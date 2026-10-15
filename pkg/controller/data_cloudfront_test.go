package controller

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func cloudFrontFixture(t *testing.T) (*blob.S3, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture-access", "fixture-secret", "")})
	objects, err := blob.NewS3(client, "fixture-bucket")
	if err != nil {
		t.Fatal(err)
	}
	return objects, key, encoded
}

func cloudFrontDecode(t *testing.T, raw string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(strings.NewReplacer("-", "+", "_", "=", "~", "/").Replace(raw))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestCloudFrontDataBackendSignedObjectProperty(t *testing.T) {
	objects, key, encoded := cloudFrontFixture(t)
	backend, err := NewCloudFrontDataBackend(objects, "cdn.example.test", "key-id", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(blob.LogDeleter); !ok {
		t.Fatal("CloudFront configuration lost physical log deletion")
	}
	property := func(input []byte, tenant uint16, size uint32) bool {
		digest := sha256.Sum256(input)
		hash := hex.EncodeToString(digest[:])
		path, err := blob.ObjectKey(fmt.Sprintf("team-%d", tenant), blob.Digest(hash))
		if err != nil {
			t.Fatal(err)
		}
		object := capability.ObjectToSign{Method: "GET", Key: path, Size: int64(size), SHA256: hash, ExpiresAt: time.Now().Add(45 * time.Second)}
		request := httptest.NewRequest("POST", "https://controller.test/api/v1/data/download", nil)
		request.Header.Set("X-Forwarded-Host", "controller.test")
		signed, err := backend.Sign(dataDownloadContext(request), object)
		if err != nil {
			t.Log(err)
			return false
		}
		u, err := url.Parse(signed.URL)
		if err != nil || u.Host != "cdn.example.test" || u.Scheme != "https" || u.Path != "/"+path || u.Query().Get("Key-Pair-Id") != "key-id" || len(u.Query()) != 3 || len(signed.Headers) != 0 || signed.Size != object.Size || signed.SHA256 != hash || !signed.ExpiresAt.Equal(object.ExpiresAt) {
			return false
		}
		policyBytes := cloudFrontDecode(t, u.Query().Get("Policy"))
		var policy sign.Policy
		if json.Unmarshal(policyBytes, &policy) != nil || len(policy.Statements) != 1 {
			return false
		}
		statement := policy.Statements[0]
		if statement.Resource != "https://cdn.example.test/"+path || statement.Condition.DateLessThan.Unix() != object.ExpiresAt.Unix() || strings.ContainsAny(statement.Resource, "*?") {
			return false
		}
		signature := cloudFrontDecode(t, u.Query().Get("Signature"))
		sum := sha1.Sum(policyBytes)
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, sum[:], signature) != nil {
			return false
		}
		changed := sha1.Sum(append(policyBytes, 'x'))
		return rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, changed[:], signature) != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudFrontDataBackendKeepsUploadConstraints(t *testing.T) {
	objects, _, encoded := cloudFrontFixture(t)
	backend, err := NewCloudFrontDataBackend(objects, "cdn.example.test", "key-id", encoded)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
	object := capability.ObjectToSign{Method: http.MethodPut, Key: "teams/tenant/pending/ticket", Size: 7, SHA256: digest, ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := backend.Sign(t.Context(), object)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed.URL)
	if err != nil || !strings.Contains(u.Host, "fixture-bucket") || u.Query().Get("X-Amz-Signature") == "" || !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "content-length") || signed.Headers["Content-Length"] != "7" {
		t.Fatalf("upload constraints: %+v err=%v", signed, err)
	}
	raw, _ := hex.DecodeString(digest)
	if signed.Headers["X-Amz-Checksum-Sha256"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("upload checksum = %v", signed.Headers)
	}
	direct := object
	direct.Method = http.MethodGet
	direct.Key = "teams/tenant/cas/" + digest
	signed, err = backend.Sign(t.Context(), direct)
	if err != nil {
		t.Fatal(err)
	}
	directURL, err := url.Parse(signed.URL)
	if err != nil || !strings.Contains(directURL.Host, "fixture-bucket") || directURL.Query().Get("Policy") != "" {
		t.Fatalf("direct download = %s err=%v", signed.URL, err)
	}

	for _, change := range []func(*capability.ObjectToSign){
		func(o *capability.ObjectToSign) { o.Method = "DELETE" },
		func(o *capability.ObjectToSign) { o.Size = -1 },
		func(o *capability.ObjectToSign) { o.SHA256 = "invalid" },
		func(o *capability.ObjectToSign) {
			o.Method = "GET"
			o.Key = "teams/tenant/cas/" + strings.Repeat("0", 64)
		},
		func(o *capability.ObjectToSign) {
			o.Method = "GET"
			o.Key = "teams/tenant/cas/" + digest
			o.ExpiresAt = time.Now().Add(61 * time.Second)
		},
		func(o *capability.ObjectToSign) { o.Key = "teams/tenant/pending/../elsewhere" },
	} {
		changed := object
		change(&changed)
		if _, err := backend.Sign(t.Context(), changed); err == nil {
			t.Fatal("invalid object reached signing")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := backend.Sign(ctx, object); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled signing = %v", err)
	}
}

func TestCloudFrontDataBackendConfigurationRefusals(t *testing.T) {
	objects, _, encoded := cloudFrontFixture(t)
	for _, domain := range []string{"", "https://cdn.example.test", "cdn.example.test/path", "user@cdn.example.test", "cdn.example.test:443", "cdn.example.test?query", "cdn.example.test#fragment", "cdn.example.test\\elsewhere", "cdn.example.test\n"} {
		if _, err := NewCloudFrontDataBackend(objects, domain, "key-id", encoded); err == nil {
			t.Fatalf("invalid domain %q accepted", domain)
		}
	}
	for _, keyID := range []string{"", "id&Policy=other", "id\n"} {
		if _, err := NewCloudFrontDataBackend(objects, "cdn.example.test", keyID, encoded); err == nil {
			t.Fatal("invalid key ID accepted")
		}
	}
	for _, key := range []string{"", "private-invalid-key"} {
		_, err := NewCloudFrontDataBackend(objects, "cdn.example.test", "key-id", key)
		if err == nil || strings.Contains(err.Error(), key) && key != "" {
			t.Fatalf("invalid key response = %v", err)
		}
	}
	if _, err := NewCloudFrontDataBackend(nil, "cdn.example.test", "key-id", encoded); err == nil {
		t.Fatal("missing backend accepted")
	}
}
