package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

func TestDirectUploadS3ReservePutCommitAndSignedGet(t *testing.T) {
	bucketName := os.Getenv("SPARKWING_S3_TEST_BUCKET")
	if bucketName == "" || os.Getenv("SPARKWING_S3_ENDPOINT") == "" {
		t.Skip("S3 integration bucket and endpoint required")
	}
	s, grant, _ := downloadFixture(t)
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "it-direct-" + hex.EncodeToString(suffix[:])
	cfg, err := config.LoadDefaultConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region == "" {
		t.Fatal("AWS_REGION or a profile region is required for the S3 integration test")
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(os.Getenv("SPARKWING_S3_ENDPOINT"))
		o.UsePathStyle = true
	})
	store, err := teamblob.New(teamblob.Options{Bucket: bucketName, Prefix: prefix, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WithSignedDownloads(store, nil, client, "", "", ""); err != nil {
		t.Fatal(err)
	}
	s.WithDirectUploads(client, bucketName, prefix)
	body := []byte("direct S3 payload")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	key := "artifacts/blobs/" + digest
	post := func(path, raw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+grant)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		return response
	}
	reserve, err := json.Marshal(map[string]any{"kind": "artifact", "key": key, "size": len(body), "sha256": digest, "run_id": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	response := post("/api/v1/data/upload", string(reserve))
	if response.Code != http.StatusOK {
		t.Fatalf("reserve = %d: %s", response.Code, response.Body.String())
	}
	var upload DirectUploadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	if upload.UploadID == "" || upload.URL == "" {
		t.Fatalf("incomplete upload reservation: %+v", upload)
	}
	createdKeys := []string{
		"pending/" + upload.UploadID,
		prefix + "/teams/team-a/local/" + key,
	}
	t.Cleanup(func() {
		clean := true
		for _, objectKey := range createdKeys {
			if _, err := client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
				Bucket: aws.String(bucketName), Key: aws.String(objectKey),
			}); err != nil {
				clean = false
				t.Errorf("delete test object %s: %v", objectKey, err)
			}
		}
		for _, objectKey := range createdKeys {
			_, err := client.HeadObject(context.Background(), &s3.HeadObjectInput{
				Bucket: aws.String(bucketName), Key: aws.String(objectKey),
			})
			var api smithy.APIError
			if !errors.As(err, &api) || (api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchKey") {
				clean = false
				t.Errorf("test object %s remains or cannot be checked: %v", objectKey, err)
			}
		}
		if clean {
			t.Logf("verified cleanup of test objects under %s", prefix)
		}
	})
	put := func(data []byte) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, upload.URL, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = int64(len(data))
		for k, v := range upload.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s3Error struct {
			Code string `xml:"Code"`
		}
		if resp.StatusCode/100 != 2 {
			if err := xml.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&s3Error); err != nil {
				return resp.StatusCode, "unparsed"
			}
		}
		return resp.StatusCode, s3Error.Code
	}
	if code, reason := put([]byte("direct S3 payloae")); code/100 == 2 {
		t.Fatalf("wrong checksum PUT = %d, want rejection", code)
	} else {
		t.Logf("wrong checksum PUT rejected: status %d, code %s", code, reason)
	}
	if code, reason := put(body); code/100 != 2 {
		t.Fatalf("valid PUT = %d, code %s", code, reason)
	} else {
		t.Logf("valid PUT accepted: status %d", code)
	}
	response = post("/api/v1/data/commit", `{"upload_id":"`+upload.UploadID+`","run_id":"run-1"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("commit = %d: %s", response.Code, response.Body.String())
	}
	t.Logf("commit accepted: status %d", response.Code)
	response = post("/api/v1/data/download", `{"kind":"artifact","key":"`+key+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("sign GET = %d: %s", response.Code, response.Body.String())
	}
	var signed DataDownloadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &signed); err != nil {
		t.Fatal(err)
	}
	if signed.SHA256 != digest || signed.Size != int64(len(body)) {
		t.Fatalf("signed object = %+v", signed)
	}
	get, err := http.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var got bytes.Buffer
	if _, err := got.ReadFrom(get.Body); err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusOK || !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("signed GET = %d %q", get.StatusCode, got.String())
	}
	t.Logf("signed GET returned verified bytes: status %d", get.StatusCode)
}
