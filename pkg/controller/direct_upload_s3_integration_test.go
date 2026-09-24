package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

func TestDirectUploadS3ReservePutCommitAndSignedGet(t *testing.T) {
	bucketName := os.Getenv("SPARKWING_S3_TEST_BUCKET")
	if bucketName == "" || os.Getenv("SPARKWING_S3_ENDPOINT") == "" {
		t.Skip("S3 integration bucket and endpoint required")
	}
	s, grant, _ := downloadFixture(t)
	prefix := "it-direct-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	client := s3.New(s3.Options{
		Region: os.Getenv("AWS_REGION"), BaseEndpoint: aws.String(os.Getenv("SPARKWING_S3_ENDPOINT")), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), ""),
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
	put := func(data []byte) int {
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
		return resp.StatusCode
	}
	if code := put([]byte("direct S3 payloae")); code/100 == 2 {
		t.Fatalf("wrong checksum PUT = %d, want rejection", code)
	}
	if code := put(body); code/100 != 2 {
		t.Fatalf("valid PUT = %d", code)
	}
	response = post("/api/v1/data/commit", `{"upload_id":"`+upload.UploadID+`","run_id":"run-1"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("commit = %d: %s", response.Code, response.Body.String())
	}
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
}
