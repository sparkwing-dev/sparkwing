package controller

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestDataStorageSignedFilesystemRoutes(t *testing.T) {
	dir := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server := New(nil, nil).WithDataObjects(objects)
	router := server.WithAuthenticator(NewAuthenticator(nil, time.Minute)).Handler()
	property := func(payload [32]byte, tenant uint16) bool {
		team := fmt.Sprintf("tenant-%d", tenant)
		digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(payload[:])))
		key := "teams/" + team + "/pending/" + string(digest)
		put, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: 32, SHA256: string(digest), ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Log(err)
			return false
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, put.URL, bytes.NewReader(payload[:])))
		if w.Code != http.StatusCreated {
			t.Logf("PUT: %d %s", w.Code, w.Body.String())
			return false
		}
		object := blob.Blob{Team: team, Digest: digest, Size: 32}
		if err := objects.CommitObject(t.Context(), key, object); err != nil {
			t.Log(err)
			return false
		}
		final, err := blob.ObjectKey(team, digest)
		if err != nil {
			t.Log(err)
			return false
		}
		get, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: final, Size: 32, SHA256: string(digest), ExpiresAt: time.Now().Add(30 * time.Second)})
		if err != nil {
			t.Log(err)
			return false
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, get.URL, nil))
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), payload[:]) {
			t.Logf("GET: %d %s", w.Code, w.Body.String())
			return false
		}
		for _, transfer := range []struct{ method, url string }{{http.MethodPut, put.URL}, {http.MethodGet, get.URL}} {
			for _, path := range []string{"/api/v1/data/uploads/other", "/api/v1/data/objects/teams/other/cas/" + string(digest), "/api/v1/log-segments/logs/other"} {
				r := httptest.NewRequest(transfer.method, transfer.url, nil)
				r.URL.Path, r.URL.RawPath = path, ""
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code >= 200 && w.Code < 300 {
					t.Logf("substituted transfer succeeded: %s", path)
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}
