package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func signingObject(method string, now time.Time) capability.ObjectToSign {
	digest := sha256.Sum256([]byte("payload"))
	return capability.ObjectToSign{Method: method, Key: "team/blobs/with space", Size: 7, SHA256: hex.EncodeToString(digest[:]), ExpiresAt: now.Add(time.Minute)}
}

func TestFilesystemSignedObjectBoundary(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	signer, err := NewFilesystemDataSigner("https://controller.example", []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now }
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			object := signingObject(method, now)
			signed, err := signer.Sign(context.Background(), object)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(method, signed.URL, nil)
			r.ContentLength = object.Size
			for key, value := range signed.Headers {
				r.Header.Set(key, value)
			}
			got, err := signer.Verify(r)
			if err != nil || got != object {
				t.Fatalf("verified=%+v err=%v", got, err)
			}
			for _, field := range []string{"size", "sha256", "expires", "signature"} {
				copy := r.Clone(r.Context())
				u := *r.URL
				copy.URL = &u
				q := copy.URL.Query()
				q.Set(field, q.Get(field)+"0")
				copy.URL.RawQuery = q.Encode()
				if _, err := signer.Verify(copy); err == nil {
					t.Fatalf("tampered %s accepted", field)
				}
			}
			copy := r.Clone(r.Context())
			copy.Method = "DELETE"
			if _, err := signer.Verify(copy); err == nil {
				t.Fatal("changed method accepted")
			}
			copy = r.Clone(r.Context())
			u := *r.URL
			copy.URL = &u
			copy.URL.Path += "other"
			if _, err := signer.Verify(copy); err == nil {
				t.Fatal("changed key accepted")
			}
			if method == "PUT" {
				copy = r.Clone(r.Context())
				copy.ContentLength++
				if _, err := signer.Verify(copy); err == nil {
					t.Fatal("changed upload size accepted")
				}
			}
			signer.now = func() time.Time { return object.ExpiresAt }
			if _, err := signer.Verify(r); err == nil {
				t.Fatal("expired URL accepted")
			}
			signer.now = func() time.Time { return now }
		})
	}
	for _, key := range []string{"", "../outside", "/absolute", "a/../b", "a//b", "a\\b"} {
		object := signingObject("GET", now)
		object.Key = key
		if _, err := signer.Sign(context.Background(), object); err == nil {
			t.Fatalf("unsafe key %q signed", key)
		}
	}
	object := signingObject("GET", now)
	object.ExpiresAt = now.Add(61 * time.Second)
	if _, err := signer.Sign(context.Background(), object); err == nil {
		t.Fatal("overlong data URL signed")
	}
	object = signingObject("PUT", now)
	object.ExpiresAt = now.Add(15 * time.Minute)
	if _, err := signer.Sign(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://controller.example/api/v1/data/objects/team/blob", nil)
	r.Header.Set("Authorization", "Bearer swk_not_a_signature")
	if _, err := signer.Verify(r); err == nil {
		t.Fatal("bearer substituted for URL proof")
	}
}

func TestS3SignedObjectBoundary(t *testing.T) {
	now := time.Now()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture-access", "fixture-secret", "")})
	signer, err := NewS3DataSigner(client, "fixture-bucket")
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now }
	for _, method := range []string{"GET", "PUT"} {
		object := signingObject(method, now)
		signed, err := signer.Sign(context.Background(), object)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(signed.URL)
		if err != nil || u.Query().Get("X-Amz-Expires") != "60" || u.Path != "/"+object.Key {
			t.Fatalf("signed URL does not bind key/expiry: %v", err)
		}
		if method == "PUT" && (!strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "content-length") || signed.Headers["x-amz-checksum-sha256"] == "") {
			t.Fatal("PUT omitted size/checksum binding")
		}
	}
}
