package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota/storagequotatest"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

const blobTestBucket = "cache-blobs"

// newBlobServer is newBudgetedServer with the blob stores in an in-memory
// bucket. It returns the raw client so a test can look at the bucket
// itself rather than trusting the service's own answers.
func newBlobServer(t *testing.T, token string) (*httptest.Server, *s3.Client) {
	t.Helper()
	srv, raw, _ := newBlobServerWith(t, token, nil)
	return srv, raw
}

// newBlobServerWith lets configure change the config before New, and also
// returns the handler so a test can serve a request it built itself.
func newBlobServerWith(t *testing.T, token string, configure func(*Config, *s3.Client)) (*httptest.Server, *s3.Client, http.Handler) {
	t.Helper()
	fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(fake.Close)
	raw := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(fake.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	if _, err := raw.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(blobTestBucket)}); err != nil {
		t.Fatal(err)
	}
	saveCounter(t)
	savedOpen, savedStore := openBlobStore, blobStore
	t.Cleanup(func() { openBlobStore, blobStore = savedOpen, savedStore })
	openBlobStore = func(_ context.Context, raw2 string) (*teamblob.Store, error) {
		if raw2 != "s3://"+blobTestBucket+"/cache" {
			t.Fatalf("opened %q", raw2)
		}
		return teamblob.New(teamblob.Options{
			Bucket: blobTestBucket, Prefix: "cache", Client: raw,
			PartSize: 5 << 20, MultipartThreshold: 5 << 20,
		})
	}
	savedMeter := egressMeter
	egressMeter = nil
	t.Cleanup(func() { egressMeter = savedMeter })
	saved := []*string{&dataRoot, &repoDir, &archDir, &artifactsDir, &binsDir, &cacheDir, &uploadsDir, &namesFile, &proxyDir, &sshKeyDir, &apiToken, &teamsDir, &grantKey}
	values := make([]string, len(saved))
	for i, p := range saved {
		values[i] = *p
	}
	t.Cleanup(func() {
		for i, p := range saved {
			*p = values[i]
		}
	})

	root := t.TempDir()
	c := DefaultConfig()
	c.DataDir = root
	c.ProxyDir = filepath.Join(root, "proxy")
	c.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	c.APIToken = token
	c.GrantKey = testGrantKey(token)
	c.BlobStore = "s3://" + blobTestBucket + "/cache"
	_, c.ControllerURL = fakeController(t, token, 1<<40, 0)
	if configure != nil {
		configure(&c, raw)
	}
	s, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)
	return srv, raw, s.handler
}

func fakeController(t *testing.T, token string, share, downloadCap int64) (*storagequotatest.Controller, string) {
	t.Helper()
	ctl := storagequotatest.New(share, downloadCap)
	ctl.Token = token
	srv := httptest.NewServer(ctl)
	t.Cleanup(srv.Close)
	return ctl, srv.URL
}

func saveCounter(t *testing.T) {
	t.Helper()
	saved, savedAuth := counter, counterAuth
	t.Cleanup(func() { counter, counterAuth = saved, savedAuth })
}

func bucketKeys(t *testing.T, raw *s3.Client) []string {
	t.Helper()
	out, err := raw.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(blobTestBucket)})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	return keys
}

// The same isolation the volume gives: two teams naming the same key land
// in two namespaces of the bucket, and neither reaches the other's.
func TestBlobStoreKeepsEachTeamsBlobsApart(t *testing.T) {
	const token = "operator-token"
	srv, raw := newBlobServer(t, token)
	teamA, teamB := grantFor(t, token, "team-a"), grantFor(t, token, "team-b")

	writes := []struct{ method, write, read string }{
		{http.MethodPut, "/bin/deadbeef", "/bin/deadbeef"},
		{http.MethodPut, "/cache/go-mod-abc", "/cache/go-mod-abc"},
		{http.MethodPost, "/artifacts/run-1?path=out.txt", "/artifacts/run-1?glob=out.txt"},
	}
	for _, w := range writes {
		if code, body := send(t, srv, w.method, w.write, teamA, "team-a secret"); code/100 != 2 {
			t.Fatalf("%s %s as team A = %d: %s", w.method, w.write, code, body)
		}
		if code, body := send(t, srv, http.MethodGet, w.read, teamA, ""); code != http.StatusOK || body != "team-a secret" {
			t.Errorf("team A reading its own %s = %d %q", w.read, code, body)
		}
		if code, body := send(t, srv, http.MethodGet, w.read, teamB, ""); code == http.StatusOK || strings.Contains(body, "team-a secret") {
			t.Errorf("team B read team A's %s: %d %q", w.read, code, body)
		}
		if code, body := send(t, srv, http.MethodGet, w.read, token, ""); strings.Contains(body, "team-a secret") {
			t.Errorf("the operator's namespace served team A's %s: %d", w.read, code)
		}
		if code, body := send(t, srv, w.method, w.write, teamB, "team-b poison"); code/100 != 2 {
			t.Fatalf("%s %s as team B = %d: %s", w.method, w.write, code, body)
		}
		if _, body := send(t, srv, http.MethodGet, w.read, teamA, ""); body != "team-a secret" {
			t.Errorf("team B's write replaced team A's %s with %q", w.read, body)
		}
	}
	for _, bad := range []string{"/artifacts/run-1?path=../../teams/team-a/bins/deadbeef", "/artifacts/a%20b?path=x", "/artifacts/run-1?path=/abs"} {
		if code, _ := send(t, srv, http.MethodPost, bad, teamB, "x"); code != http.StatusBadRequest {
			t.Errorf("POST %s as team B = %d, want 400", bad, code)
		}
	}

	want := map[string]bool{
		"cache/teams/team-a/bins/deadbeef":           true,
		"cache/teams/team-a/cache/go-mod-abc.tar.gz": true,
		"cache/teams/team-a/artifacts/run-1/out.txt": true,
		"cache/teams/team-b/bins/deadbeef":           true,
		"cache/teams/team-b/cache/go-mod-abc.tar.gz": true,
		"cache/teams/team-b/artifacts/run-1/out.txt": true,
	}
	got := bucketKeys(t, raw)
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected object %s", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("missing object %s", k)
	}
	if entries, _ := os.ReadDir(teamsDir); len(entries) != 0 {
		t.Errorf("a blob-store cache wrote team trees to the volume: %d entries", len(entries))
	}

	if code, _ := send(t, srv, http.MethodDelete, "/admin/teams/team-a", teamA, ""); code != http.StatusUnauthorized {
		t.Errorf("a grant deleted its own team: %d", code)
	}
	if code, body := send(t, srv, http.MethodDelete, "/admin/teams/team-a", token, ""); code != http.StatusNoContent {
		t.Fatalf("delete team-a = %d %s", code, body)
	}
	for _, k := range bucketKeys(t, raw) {
		if strings.HasPrefix(k, "cache/teams/team-a/") {
			t.Errorf("team-a's %s survived its deletion", k)
		}
	}
	if code, body := send(t, srv, http.MethodGet, "/cache/go-mod-abc", teamB, ""); code != http.StatusOK || body != "team-b poison" {
		t.Errorf("team-b lost its archive to team-a's deletion: %d %q", code, body)
	}
}

// An artifact past the cap is refused and leaves nothing in the bucket:
// no object and no open multipart upload.
func TestBlobStoreRefusesAnOversizedArtifactWithoutLeavingParts(t *testing.T) {
	const token = "operator-token"
	srv, raw := newBlobServer(t, token)
	saved := maxArtifactBytes
	maxArtifactBytes = 6 << 20
	t.Cleanup(func() { maxArtifactBytes = saved })
	grant := grantFor(t, token, "team-a")

	body := strings.Repeat("z", 7<<20)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/artifacts/run-1?path=big.bin", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+grant)
	// A chunked body hides its length, so the cap stops it mid-stream.
	req.ContentLength = -1
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized artifact = %d, want 413", resp.StatusCode)
	}
	if keys := bucketKeys(t, raw); len(keys) != 0 {
		t.Fatalf("a refused artifact left %v", keys)
	}
	uploads, err := raw.ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{Bucket: aws.String(blobTestBucket)})
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads.Uploads) != 0 {
		t.Fatalf("%d multipart uploads left open", len(uploads.Uploads))
	}
}

// A bucket that denies this service's writes pauses them: the next uploads
// are refused with 503 and Retry-After before their bodies are read, and
// reads keep working.
func TestBlobStoreRefusesWritesFastWhileTheBucketDeniesThem(t *testing.T) {
	const token = "operator-token"
	srv, raw := newBlobServer(t, token)
	grant := grantFor(t, token, "team-a")
	if code, body := send(t, srv, http.MethodPut, "/cache/kept", grant, "kept"); code != http.StatusCreated {
		t.Fatalf("put = %d %s", code, body)
	}
	denied := &deniedPuts{Client: raw}
	store, err := teamblob.New(teamblob.Options{Bucket: blobTestBucket, Prefix: "cache", Client: denied})
	if err != nil {
		t.Fatal(err)
	}
	blobStore = store
	if code, _ := send(t, srv, http.MethodPut, "/cache/new", grant, "x"); code != http.StatusBadGateway {
		t.Fatalf("the denied put = %d, want 502", code)
	}
	for _, w := range []struct{ method, path string }{
		{http.MethodPut, "/cache/new"},
		{http.MethodPut, "/bin/deadbeef"},
		{http.MethodPost, "/artifacts/run-1?path=out.txt"},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), w.method, srv.URL+w.path, strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer "+grant)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
			t.Errorf("%s %s while paused = %d (Retry-After %q), want 503 with Retry-After", w.method, w.path, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	}
	if n := denied.puts.Load(); n != 1 {
		t.Fatalf("a denying bucket was sent %d PUTs, want 1", n)
	}
	if code, body := send(t, srv, http.MethodGet, "/cache/kept", grant, ""); code != http.StatusOK || body != "kept" {
		t.Fatalf("a read while paused = %d %q", code, body)
	}
}

type deniedPuts struct {
	teamblob.Client
	puts atomic.Int64
}

func (d *deniedPuts) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	d.puts.Add(1)
	return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "explicit deny"}
}
