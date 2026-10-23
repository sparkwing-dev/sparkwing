package blob

import (
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestFilesystemMissingReferencedBytesFailProperty(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewFilesystem(FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	property := func(body []byte) bool {
		object := Blob{Team: "team", Digest: digest(string(body)), Size: int64(len(body))}
		key, err := ObjectKey(object.Team, object.Digest)
		if err != nil {
			return false
		}
		path := filepath.Join(dir, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := backend.VerifyBlob(t.Context(), object); err != nil {
			return false
		}
		signed, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: key, Size: object.Size, SHA256: string(object.Digest), ExpiresAt: time.Now().Add(30 * time.Second)})
		if err != nil || backend.DeleteBlob(t.Context(), object) != nil {
			return false
		}
		if err := backend.VerifyBlob(t.Context(), object); !errors.Is(err, ErrNotFound) {
			return false
		}
		if data, err := backend.ReadOutput(t.Context(), object); data != nil || !errors.Is(err, ErrNotFound) {
			return false
		}
		if members, err := backend.ReadManifest(t.Context(), object.Team, object.Digest); members != nil || !errors.Is(err, ErrNotFound) {
			return false
		}
		if err := backend.VerifyManifest(t.Context(), object.Team, object.Digest, nil); !errors.Is(err, ErrNotFound) {
			return false
		}
		response := httptest.NewRecorder()
		backend.ServeHTTP(response, httptest.NewRequest(http.MethodGet, signed.URL, nil))
		return response.Code == http.StatusNotFound
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(1193))}); err != nil {
		t.Fatal(err)
	}
}
