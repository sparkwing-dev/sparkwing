package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
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
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

var testGrantScope = &authwire.CacheScope{Refs: []string{"manual:"}}

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
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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
	if err := team.CreateTrigger(t.Context(), store.Trigger{ID: "run-1", Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_, token, err := team.CreateToken(t.Context(), "runner", store.TokenKindRunner, []string{ScopeTriggersClaim}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `UPDATE triggers SET status = 'claimed', claim_principal = ?, claim_token_prefix = ?, claim_seq = 1, lease_expires_at = ? WHERE id = ?`, token.Principal, token.Prefix, time.Now().Add(time.Hour).UnixNano(), "run-1"); err != nil {
		t.Fatal(err)
	}
	grant, err := authwire.MintClaimCacheGrant("grant-key", "team-a", "run-1", time.Now(), time.Hour, &authwire.CacheClaim{Kind: "trigger", Generation: 1, Principal: token.Principal, TokenPrefix: token.Prefix}, testGrantScope)
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
	srv := New(st, nil).WithTeamDownloadCaps(5, 5).WithCacheGrantKey("grant-key")
	if err := srv.WithSignedDownloads(bucket, client, "cdn.example.test", "KPAIR", string(pemKey)); err != nil {
		t.Fatal(err)
	}
	return srv, grant, head
}

func TestStaleGrantCannotReserveOrCommitAfterSameTokenReclaimsRun(t *testing.T) {
	s, grant, _ := downloadFixture(t)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	s.WithDirectUploads(client, "bucket", "cache")
	request := func(digest string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/data/upload", strings.NewReader(`{"kind":"binary","key":"bin/01234567-89abcdef/`+digest+`","size":4,"sha256":"`+digest+`","run_id":"run-1"}`))
		req.Header.Set("Authorization", "Bearer "+grant)
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	first := httptest.NewRecorder()
	s.Handler().ServeHTTP(first, request(strings.Repeat("a", 64)))
	if first.Code != http.StatusOK {
		t.Fatalf("first reserve = %d: %s", first.Code, first.Body.String())
	}
	var upload DirectUploadResponse
	if err := json.Unmarshal(first.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE triggers SET claim_seq = 2 WHERE id = ?`, "run-1"); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, request(strings.Repeat("b", 64)))
	if stale.Code != http.StatusForbidden {
		t.Fatalf("stale reserve = %d, want 403", stale.Code)
	}
	commit := httptest.NewRequest(http.MethodPost, "/api/v1/data/commit", strings.NewReader(`{"upload_id":"`+upload.UploadID+`","run_id":"run-1"}`))
	commit.Header.Set("Authorization", "Bearer "+grant)
	commit.Header.Set("Content-Type", "application/json")
	stale = httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, commit)
	if stale.Code != http.StatusForbidden {
		t.Fatalf("stale commit = %d, want 403", stale.Code)
	}
}

func TestPendingTriggerCanReserveBinaryUpload(t *testing.T) {
	s, grant, _ := downloadFixture(t)
	if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE runs SET status = 'pending' WHERE id = ?`, "run-1"); err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	s.WithDirectUploads(client, "bucket", "cache")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/data/upload", strings.NewReader(`{"kind":"binary","key":"bin/01234567-89abcdef/`+strings.Repeat("a", 64)+`","size":4,"sha256":"`+strings.Repeat("a", 64)+`","run_id":"run-1"}`))
	request.Header.Set("Authorization", "Bearer "+grant)
	request.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending trigger binary PUT = %d: %s", rec.Code, rec.Body.String())
	}
	artifact := httptest.NewRequest(http.MethodPost, "/api/v1/data/upload", strings.NewReader(`{"kind":"artifact","key":"artifacts/blobs/`+strings.Repeat("a", 64)+`","size":4,"sha256":"`+strings.Repeat("a", 64)+`","run_id":"run-1"}`))
	artifact.Header.Set("Authorization", "Bearer "+grant)
	artifact.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, artifact)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pending trigger artifact PUT = %d, want 403", rec.Code)
	}
}

func TestCacheGrantSigningBindsLiveTriggerGeneration(t *testing.T) {
	s, boundGrant, _ := downloadFixture(t)
	issued, err := authwire.VerifyCacheGrant("grant-key", boundGrant, time.Now())
	if err != nil || issued.Claim == nil {
		t.Fatalf("fixture grant = %+v, %v", issued, err)
	}
	request := func(generation string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-1/cache-grant", nil)
		req.SetPathValue("id", "run-1")
		req.Header.Set(store.TriggerGenerationHeader, generation)
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{
			Name: issued.Claim.Principal, TokenPrefix: issued.Claim.TokenPrefix, Kind: store.TokenKindRunner, Team: "team-a",
		}))
		rec := httptest.NewRecorder()
		s.handleRunCacheGrant(teamA).ServeHTTP(rec, req)
		return rec
	}
	live := request("1")
	if live.Code != http.StatusOK {
		t.Fatalf("live mint = %d: %s", live.Code, live.Body.String())
	}
	var response CacheGrantResponse
	if err := json.Unmarshal(live.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	grant, err := authwire.VerifyCacheGrant("grant-key", response.Grant, time.Now())
	if err != nil || grant.Claim == nil || grant.Claim.Kind != "trigger" || grant.Claim.Generation != 1 {
		t.Fatalf("bound grant = %+v, %v", grant, err)
	}
	if stale := request("2"); stale.Code != http.StatusConflict {
		t.Fatalf("unheld generation minted: %d: %s", stale.Code, stale.Body.String())
	}
}

func TestIncompleteCloudFrontConfigDoesNotAnnounceSigning(t *testing.T) {
	s, _, _ := downloadFixture(t)
	other := New(s.store, nil)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	if err := other.WithSignedDownloads(s.downloadStores[store.StorageCache], client, "cdn.example.test", "", ""); err == nil {
		t.Fatal("incomplete signing configuration was accepted")
	}
	if got := other.dataDownloadURL(); got != "" {
		t.Fatalf("incomplete signer announced %q", got)
	}
}

func TestSignerWithoutACacheBucketDoesNotAnnounceBinaryDownloads(t *testing.T) {
	s, _, _ := downloadFixture(t)
	other := New(s.store, nil)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")})
	if err := other.WithSignedDownloads(nil, client, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := other.dataDownloadURL(); got != "" {
		t.Fatalf("a signer with no cache bucket announced binary route %q", got)
	}
}
