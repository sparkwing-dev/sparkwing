package blob

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// A host whose wall clock is stepped back between signing and use (WSL2 and
// NTP steps) must still accept the fresh URL; only expiry and the signature
// decide a transfer once it is signed.
func TestFilesystemSignedURLsSurviveAWallClockStepBack(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	backend, err := NewFilesystem(FilesystemOptions{
		Directory: t.TempDir(), Endpoint: "https://controller.invalid",
		SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	object := Blob{Team: "team", Digest: digest("hello"), Size: 5}
	put, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: "teams/team/pending/ticket", Size: 5, SHA256: string(object.Digest), ExpiresAt: now.Add(limits.DataUploadURLTTL)})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, put.URL, strings.NewReader("hello")))
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	if err := backend.CommitObject(t.Context(), "teams/team/pending/ticket", object); err != nil {
		t.Fatal(err)
	}
	final, err := ObjectKey(object.Team, object.Digest)
	if err != nil {
		t.Fatal(err)
	}
	get, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: final, Size: 5, SHA256: string(object.Digest), ExpiresAt: now.Add(limits.DataDownloadURLTTL)})
	if err != nil {
		t.Fatal(err)
	}
	logKey := "logs/r90/teams/acme/runs/run-1/jobs/build/a1-g1/seg-000001.ndjson.gz"
	logURL, _, err := backend.SignLogPut(t.Context(), logKey, 11, digest("first bytes"), now.Add(limits.LogUploadURLTTL))
	if err != nil {
		t.Fatal(err)
	}
	signedAt := now
	now = signedAt.Add(-time.Millisecond)
	w = httptest.NewRecorder()
	backend.ServeHTTP(w, httptest.NewRequest(http.MethodGet, get.URL, nil))
	if w.Code != http.StatusOK || w.Body.String() != "hello" {
		t.Fatalf("GET after the clock stepped back = %d: %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, logURL, strings.NewReader("first bytes")))
	if w.Code != http.StatusCreated {
		t.Fatalf("log PUT after the clock stepped back = %d: %s", w.Code, w.Body)
	}
	now = signedAt.Add(limits.DataDownloadURLTTL)
	w = httptest.NewRecorder()
	backend.ServeHTTP(w, httptest.NewRequest(http.MethodGet, get.URL, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired GET = %d, want 403", w.Code)
	}
	now = signedAt
	if _, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: final, Size: 5, SHA256: string(object.Digest), ExpiresAt: now.Add(limits.DataDownloadURLTTL + time.Second)}); err == nil {
		t.Fatal("signing accepted a download lifetime above its ceiling")
	}
	if _, _, err := backend.SignLogPut(t.Context(), logKey, 11, digest("first bytes"), now.Add(limits.LogUploadURLTTL+time.Second)); err == nil {
		t.Fatal("signing accepted a log upload lifetime above its ceiling")
	}
}
