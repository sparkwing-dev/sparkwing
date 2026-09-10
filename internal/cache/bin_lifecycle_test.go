package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
)

func TestBinaryArtifactStoreHeadAndDelete(t *testing.T) {
	old := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = old }()
	server := httptest.NewServer(http.HandlerFunc(handleBin))
	defer server.Close()
	store := sparkwingcache.New(server.URL, "", server.Client())
	const key = "deadbeef-cafebabe"
	if found, err := store.Has(t.Context(), key); err != nil || found {
		t.Fatalf("missing Has=%v %v", found, err)
	}
	if err := store.Put(t.Context(), key, strings.NewReader("binary")); err != nil {
		t.Fatal(err)
	}
	if found, err := store.Has(t.Context(), key); err != nil || !found {
		t.Fatalf("stored Has=%v %v", found, err)
	}
	head := httptest.NewRecorder()
	handleBin(head, httptest.NewRequest(http.MethodHead, "/bin/"+key, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Digest") == "" {
		t.Fatalf("HEAD: status=%d body=%q digest=%q", head.Code, head.Body.String(), head.Header().Get("Digest"))
	}
	body, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(data) != "binary" {
		t.Fatalf("GET %q %v", data, err)
	}
	for range 2 {
		if err := store.Delete(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := store.Has(t.Context(), key); err != nil || found {
		t.Fatalf("deleted Has=%v %v", found, err)
	}
	entries, err := os.ReadDir(binsDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("deleted binary left metadata: %v %v", entries, err)
	}
	for _, method := range []string{http.MethodHead, http.MethodDelete} {
		rec := httptest.NewRecorder()
		handleBin(rec, httptest.NewRequest(method, "/bin/not-a-valid-hash", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s invalid key status %d", method, rec.Code)
		}
	}
}
