package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type downloadHead struct {
	teamblob.Client
	keys []string
}

func (h *downloadHead) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	h.keys = append(h.keys, aws.ToString(in.Key))
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(4), Metadata: map[string]string{"sha256": strings.Repeat("a", 64)}}, nil
}

func downloadFixture(t *testing.T) (*Server, string, *downloadHead) {
	t.Helper()
	t.Setenv(authwire.CacheGrantKeyEnv, "grant-key")
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.AsOperator().CreateTeam(t.Context(), "team-a"); err != nil {
		t.Fatal(err)
	}
	team, err := st.ForTeam(t.Context(), "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := team.CreateRun(t.Context(), store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	grant, err := authwire.MintCacheGrant("grant-key", "team-a", "run-1", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	head := &downloadHead{}
	bucket, err := teamblob.New(teamblob.Options{Bucket: "bucket", Prefix: "cache", Client: head})
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	srv := New(st, nil).WithTeamDownloadCaps(5, 5)
	if err := srv.WithSignedDownloads(bucket, nil, client, "cdn.example.test", "KPAIR", string(pemKey)); err != nil {
		t.Fatal(err)
	}
	return srv, grant, head
}

func callDownload(t *testing.T, s *Server, grant, key string, ingress bool) (*httptest.ResponseRecorder, DataDownloadResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/download", strings.NewReader(`{"kind":"binary","key":"`+key+`"}`))
	req.Header.Set("Authorization", "Bearer "+grant)
	req.Header.Set("Content-Type", "application/json")
	if ingress {
		req.Header.Set("X-Forwarded-For", "192.0.2.1")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body DataDownloadResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return rec, body
}

func TestDataDownloadRejectsInvalidGrant(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/download", strings.NewReader(`{"kind":"binary","key":"bins/abc"}`))
	req.Header.Set("Authorization", "Bearer swcg1.invalid")
	rec := httptest.NewRecorder()
	s := New(st, nil)
	s.downloadStores = map[store.StorageKind]*teamblob.Store{store.StorageCache: nil}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestDataDownloadSignsOnlyGrantsTeamAndChargesAtSigning(t *testing.T) {
	s, grant, head := downloadFixture(t)
	bad, _ := callDownload(t, s, grant, "teams/team-b/bins/abc", false)
	if bad.Code != http.StatusBadRequest || len(head.keys) != 0 {
		t.Fatalf("cross-team key: status=%d head=%v", bad.Code, head.keys)
	}
	first, body := callDownload(t, s, grant, "bins/abc", false)
	if first.Code != http.StatusOK {
		t.Fatalf("first=%d: %s", first.Code, first.Body.String())
	}
	if len(head.keys) != 1 || head.keys[0] != "cache/teams/team-a/bins/abc" {
		t.Fatalf("head keys=%v", head.keys)
	}
	u, err := url.Parse(body.URL)
	if err != nil || !strings.Contains(u.Path, "/cache/teams/team-a/bins/abc") || u.Query().Get("X-Amz-Expires") != "60" {
		t.Fatalf("S3 URL=%q err=%v", body.URL, err)
	}
	if body.Size != 4 || body.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("body=%+v", body)
	}
	second, _ := callDownload(t, s, grant, "bins/abc", false)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second=%d: %s", second.Code, second.Body.String())
	}
}

func TestDataDownloadIngressGetsExactExpiringCloudFrontPolicy(t *testing.T) {
	s, grant, _ := downloadFixture(t)
	rec, body := callDownload(t, s, grant, "bins/abc", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(body.URL)
	if err != nil || u.Host != "cdn.example.test" || u.Query().Get("Key-Pair-Id") != "KPAIR" {
		t.Fatalf("CloudFront URL=%q err=%v", body.URL, err)
	}
	encoded := strings.NewReplacer("-", "+", "_", "=", "~", "/").Replace(u.Query().Get("Policy"))
	policy, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Statement []struct {
			Resource  string                      `json:"Resource"`
			Condition map[string]map[string]int64 `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal(policy, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Statement) != 1 || parsed.Statement[0].Resource != "https://cdn.example.test/cache/teams/team-a/bins/abc" {
		t.Fatalf("policy=%s", policy)
	}
	if got := parsed.Statement[0].Condition["DateLessThan"]["AWS:EpochTime"]; got != body.Expires.Unix() {
		t.Fatalf("expiry=%d response=%d", got, body.Expires.Unix())
	}
}

func TestDataDownloadRejectsGrantForFinishedRun(t *testing.T) {
	s, grant, head := downloadFixture(t)
	if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE runs SET status = 'success' WHERE id = ?`, "run-1"); err != nil {
		t.Fatal(err)
	}
	rec, _ := callDownload(t, s, grant, "bins/abc", false)
	if rec.Code != http.StatusForbidden || len(head.keys) != 0 {
		t.Fatalf("finished run: status=%d heads=%v", rec.Code, head.keys)
	}
}

func TestDataDownloadForwardedHostUsesCloudFront(t *testing.T) {
	s, grant, _ := downloadFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/download", strings.NewReader(`{"kind":"binary","key":"bins/abc"}`))
	req.Header.Set("Authorization", "Bearer "+grant)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "app.example.test")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
	}
	var body DataDownloadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(body.URL)
	if err != nil || u.Host != "cdn.example.test" {
		t.Fatalf("URL=%q err=%v", body.URL, err)
	}
}

func TestIncompleteCloudFrontConfigDoesNotAnnounceSigning(t *testing.T) {
	s, _, _ := downloadFixture(t)
	other := New(s.store, nil)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	if err := other.WithSignedDownloads(s.downloadStores[store.StorageCache], nil, client, "cdn.example.test", "", ""); err == nil {
		t.Fatal("incomplete signing configuration was accepted")
	}
	if got := other.dataDownloadURL(); got != "" {
		t.Fatalf("incomplete signer announced %q", got)
	}
}

func TestLogsOnlySignerDoesNotAnnounceBinaryDownloads(t *testing.T) {
	s, _, _ := downloadFixture(t)
	other := New(s.store, nil)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	if err := other.WithSignedDownloads(nil, s.downloadStores[store.StorageCache], client, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := other.dataDownloadURL(); got != "" {
		t.Fatalf("logs-only signer announced binary route %q", got)
	}
}

func TestS3OnlySignerAnnouncesOnlyInCluster(t *testing.T) {
	s, _, _ := downloadFixture(t)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	if err := s.WithSignedDownloads(s.downloadStores[store.StorageCache], nil, client, "", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ingress bool
		want    string
	}{{false, "/api/v1/data/download"}, {true, ""}} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
		if tc.ingress {
			req.Header.Set("X-Forwarded-For", "192.0.2.1")
		}
		rec := httptest.NewRecorder()
		s.handleServices(rec, req)
		if tc.want == "" && rec.Code == http.StatusNotFound {
			continue
		}
		var body ServicesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.DataDownloadURL != tc.want {
			t.Fatalf("ingress=%v route=%q want %q", tc.ingress, body.DataDownloadURL, tc.want)
		}
	}
}
