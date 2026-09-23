package cache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// tierController answers the cache's storage tier route for each team, as
// the controller does, and checks the cache asks with its operator token.
func tierController(t *testing.T, token string, standings map[string]storagequota.Standing) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		team := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/teams/"), "/storage-tier")
		s, ok := standings[team]
		if !ok {
			http.Error(w, "unknown team", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(s)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A free team's allowance of 4096 bytes leaves the cache a 3072-byte share.
func newQuotaServer(t *testing.T) (*httptest.Server, *s3.Client, http.Handler, string) {
	t.Helper()
	const token = "operator-token"
	controller := tierController(t, token, map[string]storagequota.Standing{
		"free":   {Tier: storagequota.TierFree, AllowanceBytes: 4096},
		"paying": {Tier: storagequota.TierFunded, AllowanceBytes: 4096},
		"none":   {Tier: storagequota.TierNone, AllowanceBytes: 4096},
	})
	srv, raw, h := newBlobServerWith(t, token, func(c *Config, _ *s3.Client) { c.ControllerURL = controller })
	return srv, raw, h, token
}

type countingBody struct {
	r    io.Reader
	read atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *countingBody) Close() error { return nil }

// A write whose declared length passes the team's room is refused before one
// byte of it is read, and a binary is refused before it is staged.
func TestAWriteOverTheShareIsRefusedBeforeItsBodyIsRead(t *testing.T) {
	_, raw, h, token := newQuotaServer(t)
	grant := grantFor(t, token, "free")
	for _, path := range []string{"/cache/go-mod-abc", "/bin/deadbeef"} {
		body := &countingBody{r: strings.NewReader(strings.Repeat("x", 3073))}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, body)
		req.ContentLength = 3073
		req.Header.Set("Authorization", "Bearer "+grant)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "add credits") {
			t.Fatalf("%s past the share = %d %q, want 413 naming the remedy", path, rec.Code, rec.Body.String())
		}
		if n := body.read.Load(); n != 0 {
			t.Fatalf("%s read %d bytes of a refused body", path, n)
		}
	}
	if staged, _ := os.ReadDir(blobScratchDir()); len(staged) != 0 {
		t.Fatalf("a refused binary left %d staged files", len(staged))
	}
	if keys := bucketKeys(t, raw); len(keys) != 0 {
		t.Fatalf("refused writes left %v", keys)
	}
}

// The cache counts its own bucket and nothing else: a free team stores up to
// exactly its share, then the next byte is refused whatever the controller
// knows of its events and logs.
func TestAFreeTeamStoresUpToItsCacheShare(t *testing.T) {
	srv, _, _, token := newQuotaServer(t)
	grant := grantFor(t, token, "free")
	if code, body := send(t, srv, http.MethodPut, "/cache/first", grant, strings.Repeat("a", 3000)); code != http.StatusCreated {
		t.Fatalf("3000 of 3072 = %d: %s", code, body)
	}
	if code, _ := send(t, srv, http.MethodPost, "/artifacts/run-1?path=over.txt", grant, strings.Repeat("b", 73)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("73 more past the share = %d, want 413", code)
	}
	if code, body := send(t, srv, http.MethodPost, "/artifacts/run-1?path=fits.txt", grant, strings.Repeat("b", 72)); code/100 != 2 {
		t.Fatalf("the last 72 bytes = %d: %s", code, body)
	}
	if got := blobQuota.FreeUsedBytes(); got != 3072 {
		t.Fatalf("free bytes the cache answers for = %d, want 3072", got)
	}
}

// An upload of unknown length gets the room left and is cut past it; the
// cut fails the upload, so the bucket holds nothing of it and the
// reservation is given back.
func TestAnUploadOfUnknownLengthIsCutAtTheRoomLeft(t *testing.T) {
	srv, raw, _, token := newQuotaServer(t)
	grant := grantFor(t, token, "free")
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/artifacts/run-1?path=big.bin",
		strings.NewReader(strings.Repeat("z", 5000)))
	req.Header.Set("Authorization", "Bearer "+grant)
	req.ContentLength = -1
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a chunked body past the share = %d, want 413", resp.StatusCode)
	}
	if keys := bucketKeys(t, raw); len(keys) != 0 {
		t.Fatalf("a cut upload left %v", keys)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/after", grant, strings.Repeat("a", 3072)); code != http.StatusCreated {
		t.Fatalf("the whole share after a cut upload = %d: %s", code, body)
	}
}

// Negative controls: a funded team and the operator store past the share,
// and a team with no slot stores nothing.
func TestTheCacheShareBindsOnlyFreeTeams(t *testing.T) {
	srv, _, _, token := newQuotaServer(t)
	big := strings.Repeat("p", 5000)
	if code, body := send(t, srv, http.MethodPut, "/cache/big", grantFor(t, token, "paying"), big); code != http.StatusCreated {
		t.Fatalf("a funded team past the share = %d: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/big", token, big); code != http.StatusCreated {
		t.Fatalf("the operator past the share = %d: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/small", grantFor(t, token, "none"), "x"); code != http.StatusPaymentRequired ||
		!strings.Contains(body, "join the waitlist") {
		t.Fatalf("a team with no slot = %d %q, want 402", code, body)
	}
}

// A cache that holds teams to a share lists its bucket before it serves, so
// the first upload after a start is judged against what the bucket holds
// rather than an empty count.
func TestACacheCountsItsBucketBeforeItServes(t *testing.T) {
	const token = "operator-token"
	controller := tierController(t, token, map[string]storagequota.Standing{
		"free": {Tier: storagequota.TierFree, AllowanceBytes: 4096},
	})
	srv, _, _ := newBlobServerWith(t, token, func(c *Config, raw *s3.Client) {
		c.ControllerURL = controller
		if _, err := raw.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: aws.String(blobTestBucket), Key: aws.String("cache/teams/free/cache/old.tar.gz"),
			Body: strings.NewReader(strings.Repeat("o", 3000)),
		}); err != nil {
			t.Fatal(err)
		}
	})
	if code, _ := send(t, srv, http.MethodPut, "/cache/new", grantFor(t, token, "free"), strings.Repeat("n", 100)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("100 bytes past what the bucket already holds, right after start = %d, want 413", code)
	}
}

// A cache that holds teams to a share and cannot count its bucket refuses to
// start rather than serve with an empty count.
func TestACacheThatCannotCountItsBucketRefusesToStart(t *testing.T) {
	const token = "operator-token"
	fake := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(fake.Close)
	savedOpen, savedStore, savedQuota := openBlobStore, blobStore, blobQuota
	t.Cleanup(func() { openBlobStore, blobStore, blobQuota = savedOpen, savedStore, savedQuota })
	openBlobStore = func(context.Context, string) (*teamblob.Store, error) {
		return teamblob.New(teamblob.Options{
			Bucket: blobTestBucket, Prefix: "cache", ReconcileAtStart: true,
			Client: s3.New(s3.Options{
				Region: "us-east-1", BaseEndpoint: aws.String(fake.URL), UsePathStyle: true,
				Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
			}),
		})
	}
	root := t.TempDir()
	c := DefaultConfig()
	c.DataDir, c.ProxyDir, c.SSHKeyDir = root, root+"/proxy", root+"/no-ssh-key"
	c.APIToken, c.GrantKey, c.BlobStore = token, testGrantKey(token), "s3://"+blobTestBucket+"/cache"
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("New with an unlistable bucket = %v, want a refusal to start naming the count", err)
	}
}
