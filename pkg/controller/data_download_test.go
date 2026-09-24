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
	"sync/atomic"
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
	grant, err := authwire.MintClaimCacheGrant("grant-key", "team-a", "run-1", time.Now(), time.Hour, &authwire.CacheClaim{Kind: "trigger", Generation: 1, Principal: token.Principal, TokenPrefix: token.Prefix})
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

func logDownloadFixture(t *testing.T) (*Server, string, *downloadHead) {
	t.Helper()
	s, grant, head := downloadFixture(t)
	s.WithAuthenticator(NewAuthenticator(s.store, 0))
	logs, err := teamblob.New(teamblob.Options{Bucket: "bucket", Prefix: "logs", Client: head})
	if err != nil {
		t.Fatal(err)
	}
	s.downloadStores[store.StorageLogs] = logs
	return s, grant, head
}

func callLogDownload(t *testing.T, s *Server, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/download", strings.NewReader(`{"kind":"log","key":"runs/run-B/build.log"}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDataDownloadRejectsLogSigningWithRunCacheGrant(t *testing.T) {
	s, grant, head := logDownloadFixture(t)
	response := callLogDownload(t, s, grant)
	if response.Code != http.StatusForbidden || len(head.keys) != 0 {
		t.Fatalf("cross-run log signing = %d, HEAD keys=%v", response.Code, head.keys)
	}
}

func TestDataDownloadRequiresLogsReadForLogSigning(t *testing.T) {
	s, _, head := logDownloadFixture(t)
	team, err := s.store.ForTeam(t.Context(), "team-a")
	if err != nil {
		t.Fatal(err)
	}
	runsOnly, _, err := team.CreateToken(t.Context(), "reader", store.TokenKindUser, []string{ScopeRunsRead}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	response := callLogDownload(t, s, runsOnly)
	if response.Code != http.StatusForbidden || len(head.keys) != 0 {
		t.Fatalf("runs.read-only log signing = %d, HEAD keys=%v", response.Code, head.keys)
	}
	logsReader, _, err := team.CreateToken(t.Context(), "logs-reader", store.TokenKindUser, []string{ScopeLogsRead}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	response = callLogDownload(t, s, logsReader)
	if response.Code != http.StatusOK || len(head.keys) != 1 {
		t.Fatalf("logs.read signing = %d, HEAD keys=%v", response.Code, head.keys)
	}
}

func TestRevokedClaimantCannotSignDataWithAnOldGrant(t *testing.T) {
	s, grant, head := downloadFixture(t)
	issued, err := authwire.VerifyCacheGrant("grant-key", grant, time.Now())
	if err != nil || issued.Claim == nil {
		t.Fatalf("fixture grant = %+v, %v", issued, err)
	}
	var s3Calls atomic.Int32
	objectStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer objectStore.Close()
	client := s3.New(s3.Options{
		Region: "us-west-2", BaseEndpoint: aws.String(objectStore.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	})
	s.WithDirectUploads(client, "bucket", "cache")
	upload := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/data/upload", strings.NewReader(`{"kind":"binary","key":"bin/01234567-89abcdef/`+strings.Repeat("a", 64)+`","size":4,"sha256":"`+strings.Repeat("a", 64)+`","run_id":"run-1"}`))
		req.Header.Set("Authorization", "Bearer "+grant)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	first := upload()
	if first.Code != http.StatusOK {
		t.Fatalf("initial reserve = %d: %s", first.Code, first.Body.String())
	}
	var reserved DirectUploadResponse
	if err := json.Unmarshal(first.Body.Bytes(), &reserved); err != nil {
		t.Fatal(err)
	}
	if err := s.store.RevokeToken(issued.Claim.TokenPrefix, time.Now()); err != nil {
		t.Fatal(err)
	}
	if response, _ := callDownload(t, s, grant, "bins/abc", false); response.Code != http.StatusForbidden || len(head.keys) != 0 {
		t.Fatalf("revoked download = %d, HEAD keys=%v", response.Code, head.keys)
	}
	if response := upload(); response.Code != http.StatusForbidden {
		t.Fatalf("revoked reserve = %d, want 403", response.Code)
	}
	var reservations int
	if err := s.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM uploads WHERE team = ?`, "team-a").Scan(&reservations); err != nil || reservations != 1 {
		t.Fatalf("upload reservations = %d, %v", reservations, err)
	}
	commit := httptest.NewRequest(http.MethodPost, "/api/v1/data/commit", strings.NewReader(`{"upload_id":"`+reserved.UploadID+`","run_id":"run-1"}`))
	commit.Header.Set("Authorization", "Bearer "+grant)
	commit.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, commit)
	if rec.Code != http.StatusForbidden || s3Calls.Load() != 0 {
		t.Fatalf("revoked commit = %d, S3 calls=%d", rec.Code, s3Calls.Load())
	}
}

func TestDataDownloadRejectsInvalidGrant(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/download", strings.NewReader(`{"kind":"binary","key":"bins/abc"}`))
	req.Header.Set("Authorization", "Bearer swcg1.invalid")
	req.Header.Set("Content-Type", "application/json")
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

func TestCommittedBinaryDownloadUsesTheSameDailyCap(t *testing.T) {
	s, grant, _ := downloadFixture(t)
	input := "01234567-89abcdef"
	digest := strings.Repeat("a", 64)
	u, err := s.store.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "run-1", Kind: store.StorageCache,
		Key: "bin/" + input + "/" + digest, Size: 4, SHA256: digest,
		Principal: "runner", Provenance: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CommitUpload(t.Context(), "team-a", u.ID, u.Principal, time.Now()); err != nil {
		t.Fatal(err)
	}
	first, signed := callDownload(t, s, grant, "bin/"+input, false)
	if first.Code != http.StatusOK || !strings.Contains(signed.URL, "/local/bin/"+input) {
		t.Fatalf("first committed download = %d %+v", first.Code, signed)
	}
	second, _ := callDownload(t, s, grant, "bin/"+input, false)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second committed download = %d, want 429", second.Code)
	}
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

func TestDataDownloadRejectsFormerClaimantsGrant(t *testing.T) {
	s, grant, head := downloadFixture(t)
	if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE triggers SET claim_principal = ?, claim_token_prefix = ?, claim_seq = 2, lease_expires_at = ? WHERE id = ?`, "new-runner", "new-token", time.Now().Add(time.Hour).UnixNano(), "run-1"); err != nil {
		t.Fatal(err)
	}
	rec, _ := callDownload(t, s, grant, "bins/abc", false)
	if rec.Code != http.StatusForbidden || len(head.keys) != 0 {
		t.Fatalf("former claimant: status=%d heads=%v body=%s", rec.Code, head.keys, rec.Body.String())
	}
}

func TestDataDownloadRejectsUnboundCacheGrant(t *testing.T) {
	s, _, head := downloadFixture(t)
	grant, err := authwire.MintCacheGrant("grant-key", "team-a", "run-1", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := callDownload(t, s, grant, "bins/abc", false)
	if rec.Code != http.StatusForbidden || len(head.keys) != 0 {
		t.Fatalf("unbound grant: status=%d heads=%v", rec.Code, head.keys)
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

func TestCacheGrantSigningBindsLiveNodeGeneration(t *testing.T) {
	s, _, head := downloadFixture(t)
	team, err := s.store.ForTeam(t.Context(), "team-a")
	if err != nil {
		t.Fatal(err)
	}
	_, nodeToken, err := team.CreateToken(t.Context(), "node-runner", store.TokenKindRunner, []string{ScopeNodesClaim}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateNode(t.Context(), store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE nodes SET claimed_by = ?, claim_principal = ?, claim_token_prefix = ?, claim_membership_id = ?, reservation_id = ?, claim_generation = 1, lease_expires_at = ? WHERE run_id = ? AND node_id = ?`, "holder", nodeToken.Principal, nodeToken.Prefix, "member", "reservation", time.Now().Add(time.Hour).UnixNano(), "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-1/cache-grant", nil)
	req.SetPathValue("id", "run-1")
	req.Header.Set(store.ClaimHolderHeader, "holder")
	req.Header.Set(store.ClaimMembershipHeader, "member")
	req.Header.Set(store.ClaimReservationHeader, "reservation")
	req.Header.Set(store.ClaimGenerationHeader, "1")
	req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{Name: nodeToken.Principal, TokenPrefix: nodeToken.Prefix, Kind: store.TokenKindRunner, Team: "team-a"}))
	rec := httptest.NewRecorder()
	s.handleRunCacheGrant(teamA).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("node mint = %d: %s", rec.Code, rec.Body.String())
	}
	var response CacheGrantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	grant, err := authwire.VerifyCacheGrant("grant-key", response.Grant, time.Now())
	if err != nil || grant.Claim == nil || grant.Claim.Kind != "node" || grant.Claim.NodeID != "build" || grant.Claim.TokenPrefix != nodeToken.Prefix {
		t.Fatalf("node grant = %+v, %v", grant, err)
	}
	if signed, _ := callDownload(t, s, response.Grant, "bins/abc", false); signed.Code != http.StatusOK {
		t.Fatalf("live node sign = %d: %s", signed.Code, signed.Body.String())
	}
	if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE nodes SET claim_generation = 2 WHERE run_id = ? AND node_id = ?`, "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	if stale, _ := callDownload(t, s, response.Grant, "bins/abc", false); stale.Code != http.StatusForbidden || len(head.keys) != 1 {
		t.Fatalf("stale node sign = %d, heads=%v", stale.Code, head.keys)
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
