package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBufferedBodiesRejectOverflow(t *testing.T) {
	oldLimit := maxBufferedBodyBytes
	maxBufferedBodyBytes = 32
	t.Cleanup(func() { maxBufferedBodyBytes = oldLimit })
	for _, size := range []int{32, 33} {
		t.Run(fmt.Sprintf("bytes-%d", size), func(t *testing.T) {
			payload := strings.Repeat("x", size)
			t.Run("proxy", func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.(http.Flusher).Flush()
					_, _ = w.Write([]byte(payload))
				}))
				defer upstream.Close()
				withTestProxy(t, map[string]Registry{"test": {Name: "test", Upstream: upstream.URL}}, func() {
					w := httptest.NewRecorder()
					handleProxy(w, httptest.NewRequest(http.MethodGet, "/proxy/test/archive.tgz", nil))
					want := http.StatusOK
					if size > 32 {
						want = http.StatusBadGateway
					}
					if w.Code != want {
						t.Errorf("status = %d, want %d", w.Code, want)
					}
					if size == 32 && w.Body.String() != payload {
						t.Errorf("body = %q", w.Body.String())
					}
					entries, err := os.ReadDir(filepath.Join(proxyDir, "test"))
					if err != nil {
						t.Fatal(err)
					}
					if size > 32 && len(entries) != 0 {
						t.Errorf("oversized upstream left %d cache entries", len(entries))
					}
				})
			})
			t.Run("upload", func(t *testing.T) {
				oldDir := uploadsDir
				uploadsDir = t.TempDir()
				t.Cleanup(func() { uploadsDir = oldDir })
				w := httptest.NewRecorder()
				handleUpload(w, httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(payload)))
				want := http.StatusOK
				if size > 32 {
					want = http.StatusRequestEntityTooLarge
				}
				if w.Code != want {
					t.Errorf("status = %d, want %d", w.Code, want)
				}
				entries, err := os.ReadDir(uploadsDir)
				if err != nil {
					t.Fatal(err)
				}
				if size > 32 && len(entries) != 0 {
					t.Errorf("oversized upload left %d files", len(entries))
				}
			})
		})
	}
}
