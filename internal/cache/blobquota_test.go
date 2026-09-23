package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota/storagequotatest"
)

func newQuotaServer(t *testing.T) (*httptest.Server, *s3.Client, http.Handler, *storagequotatest.Controller, string) {
	t.Helper()
	const token = "operator-token"
	ctl, url := fakeController(t, token, 3072, 0)
	ctl.Tiers["paying"] = storagequota.TierFunded
	ctl.Tiers["none"] = storagequota.TierNone
	srv, raw, h := newBlobServerWith(t, token, func(c *Config, _ *s3.Client) { c.ControllerURL = url })
	return srv, raw, h, ctl, token
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
	_, raw, h, _, token := newQuotaServer(t)
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

// A free team stores up to exactly its share as the controller counts it,
// then the next byte is refused; the cache asks with its own token.
func TestAFreeTeamStoresUpToItsCacheShare(t *testing.T) {
	srv, _, _, ctl, token := newQuotaServer(t)
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
	if used, reserved := ctl.Held("free", storagequota.KindCache); used != 3072 || reserved != 0 {
		t.Fatalf("the controller counts %d used, %d reserved; want 3072 and nothing held", used, reserved)
	}
	for _, auth := range ctl.Auths() {
		if auth != "Bearer "+token {
			t.Fatalf("a counter call carried %q, want the cache's own token", auth)
		}
	}
}

// An overwrite commits what it changed, so a smaller rewrite of a key gives
// the difference back.
func TestAnOverwriteCommitsTheDifference(t *testing.T) {
	srv, _, _, ctl, token := newQuotaServer(t)
	grant := grantFor(t, token, "free")
	if code, body := send(t, srv, http.MethodPut, "/cache/key", grant, strings.Repeat("a", 3000)); code != http.StatusCreated {
		t.Fatalf("first write = %d: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/key", grant, strings.Repeat("a", 72)); code != http.StatusCreated {
		t.Fatalf("a rewrite that fits = %d: %s", code, body)
	}
	if used, _ := ctl.Held("free", storagequota.KindCache); used != 72 {
		t.Fatalf("after a shrinking rewrite the controller counts %d, want 72", used)
	}
}

// An upload of unknown length gets the room left and is cut past it; the
// cut fails the upload, so the bucket holds nothing of it and the
// reservation is given back.
func TestAnUploadOfUnknownLengthIsCutAtTheRoomLeft(t *testing.T) {
	srv, raw, _, ctl, token := newQuotaServer(t)
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
	if used, reserved := ctl.Held("free", storagequota.KindCache); used != 0 || reserved != 0 {
		t.Fatalf("a cut upload left %d used, %d reserved", used, reserved)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/after", grant, strings.Repeat("a", 3072)); code != http.StatusCreated {
		t.Fatalf("the whole share after a cut upload = %d: %s", code, body)
	}
}

// Negative controls: a funded team and the operator store past the share,
// and a team with no slot stores nothing.
func TestTheCacheShareBindsOnlyFreeTeams(t *testing.T) {
	srv, _, _, _, token := newQuotaServer(t)
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

// While the controller cannot count, a free team's write is refused with
// 503 and stores nothing; the operator writes on, and so does a team the
// controller answered funded for moments before.
func TestWritesFailClosedWhileTheControllerCannotCount(t *testing.T) {
	srv, raw, _, ctl, token := newQuotaServer(t)
	paying := grantFor(t, token, "paying")
	if code, body := send(t, srv, http.MethodPut, "/cache/before", paying, "p"); code != http.StatusCreated {
		t.Fatalf("a funded write with the controller up = %d: %s", code, body)
	}
	ctl.SetDown(true)
	if code, body := send(t, srv, http.MethodPut, "/cache/down", grantFor(t, token, "free"), "f"); code != http.StatusServiceUnavailable {
		t.Fatalf("a free team's write with the controller down = %d %q, want 503", code, body)
	}
	for _, k := range bucketKeys(t, raw) {
		if strings.Contains(k, "/free/") {
			t.Fatalf("a refused write left %s", k)
		}
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/down", token, "o"); code != http.StatusCreated {
		t.Fatalf("the operator's write with the controller down = %d: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPut, "/cache/down", paying, "p"); code != http.StatusCreated {
		t.Fatalf("a recently funded team's write with the controller down = %d: %s", code, body)
	}
	ctl.SetDown(false)
	if code, body := send(t, srv, http.MethodPut, "/cache/up", grantFor(t, token, "free"), "f"); code != http.StatusCreated {
		t.Fatalf("a free team's write with the controller back = %d: %s", code, body)
	}
}

// A cache that verifies grants and keeps a bucket cannot count its teams'
// bytes without a controller, so it refuses to start.
func TestACacheWithGrantsAndABucketNeedsAController(t *testing.T) {
	root := t.TempDir()
	saveCounter(t)
	c := DefaultConfig()
	c.DataDir, c.ProxyDir, c.SSHKeyDir = root, root+"/proxy", root+"/no-ssh-key"
	c.APIToken, c.GrantKey, c.BlobStore = "t", testGrantKey("t"), "s3://"+blobTestBucket+"/cache"
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "--controller") {
		t.Fatalf("New without a controller = %v, want a refusal naming --controller", err)
	}
}
