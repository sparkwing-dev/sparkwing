package cache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

const blobTestBucket = "cache-blobs"

// newBlobServer is newBudgetedServer with the blob stores in an in-memory
// bucket. It returns the raw client so a test can look at the bucket
// itself rather than trusting the service's own answers.
func newBlobServer(t *testing.T, token string, presignFrom int64) (*httptest.Server, *s3.Client) {
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
	savedOpen, savedStore, savedMin, savedTTL := openBlobStore, blobStore, presignMinBytes, presignTTL
	t.Cleanup(func() {
		openBlobStore, blobStore, presignMinBytes, presignTTL = savedOpen, savedStore, savedMin, savedTTL
	})
	openBlobStore = func(_ context.Context, raw2 string) (*teamblob.Store, error) {
		if raw2 != "s3://"+blobTestBucket+"/cache" {
			t.Fatalf("opened %q", raw2)
		}
		return teamblob.New(teamblob.Options{
			Bucket: blobTestBucket, Prefix: "cache", Client: raw,
			Presigner: s3.NewPresignClient(raw),
			PartSize:  5 << 20, MultipartThreshold: 5 << 20,
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
	c.PresignMinBytes = presignFrom
	s, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)
	return srv, raw
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
	srv, raw := newBlobServer(t, token, 0)
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

	code, body := send(t, srv, http.MethodGet, "/admin/usage?team=team-a", token, "")
	if code != http.StatusOK {
		t.Fatalf("usage = %d %s", code, body)
	}
	var usage struct {
		Teams map[string]teamblob.TeamUsage `json:"teams"`
	}
	if err := json.Unmarshal([]byte(body), &usage); err != nil {
		t.Fatal(err)
	}
	if u := usage.Teams["team-a"]; u.Objects != 3 || u.Bytes != 3*int64(len("team-a secret")) {
		t.Errorf("team-a usage = %+v", u)
	}
	if code, _ := send(t, srv, http.MethodGet, "/admin/usage", teamA, ""); code != http.StatusUnauthorized {
		t.Errorf("a grant read the usage route: %d", code)
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

// A large read leaves through a presigned URL for that one object, after
// the grant has confined the caller to its team's namespace.
func TestBlobStorePresignsLargeReadsForTheCallersTeamOnly(t *testing.T) {
	const token = "operator-token"
	srv, _ := newBlobServer(t, token, 4)
	teamA, teamB := grantFor(t, token, "team-a"), grantFor(t, token, "team-b")
	if code, body := send(t, srv, http.MethodPut, "/cache/big", teamA, "0123456789"); code != http.StatusCreated {
		t.Fatalf("put = %d %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/tiny", teamA, "abc"); code != http.StatusCreated {
		t.Fatalf("put = %d %s", code, body)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, bearer string) (int, string) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	code, loc := get("/cache/big", teamA)
	if code != http.StatusTemporaryRedirect || !strings.Contains(loc, "/cache/teams/team-a/cache/big.tar.gz") || !strings.Contains(loc, "X-Amz-Expires=300") {
		t.Fatalf("large read = %d %s", code, loc)
	}
	if code, _ := get("/cache/tiny", teamA); code != http.StatusOK {
		t.Errorf("a read under the threshold = %d, want it served directly", code)
	}
	if code, loc := get("/cache/big", teamB); code != http.StatusNotFound || loc != "" {
		t.Errorf("team B's read of team A's key = %d %s", code, loc)
	}
	// Followed, the redirect reads the object.
	if code, body := send(t, srv, http.MethodGet, "/cache/big", teamA, ""); code != http.StatusOK || body != "0123456789" {
		t.Errorf("followed read = %d %q", code, body)
	}
}

// An artifact past the cap is refused and leaves nothing in the bucket:
// no object and no open multipart upload.
func TestBlobStoreRefusesAnOversizedArtifactWithoutLeavingParts(t *testing.T) {
	const token = "operator-token"
	srv, raw := newBlobServer(t, token, 0)
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
