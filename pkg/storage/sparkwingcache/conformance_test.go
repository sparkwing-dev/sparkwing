package sparkwingcache

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
)

func TestConformance_ArtifactStore(t *testing.T) {
	conformance.TestArtifactStore(t, func() storage.ArtifactStore {
		var mu sync.Mutex
		blobs := map[string][]byte{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.URL.Path, "/bin/")
			mu.Lock()
			defer mu.Unlock()
			switch r.Method {
			case http.MethodPut:
				body, _ := io.ReadAll(r.Body)
				blobs[key] = body
				w.WriteHeader(http.StatusCreated)
			case http.MethodGet:
				b, ok := blobs[key]
				if !ok {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(b)
			case http.MethodHead:
				if _, ok := blobs[key]; !ok {
					http.NotFound(w, r)
					return
				}
			case http.MethodDelete:
				delete(blobs, key)
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		}))
		t.Cleanup(srv.Close)
		return New(srv.URL, "tok", nil)
	})
}

var _ = bytes.NewReader
