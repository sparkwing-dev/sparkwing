package cache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// The controller-typed cache backend is only exercised end to end here:
// the adapter's key handling and the /bin/ route's key validation are
// written in different packages and each looks correct on its own.
func TestControllerCacheArtifactRoundTripAgainstTheBinRoute(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	srv := httptest.NewServer(http.HandlerFunc(handleBin))
	defer srv.Close()

	ctx := context.Background()
	spec := backends.Spec{Type: backends.TypeController, Controller: "cache-profile"}
	lookup := func(string) (string, string, error) { return srv.URL, "writer-token", nil }
	store, err := storeurl.OpenArtifactStoreFromSpec(ctx, spec, lookup)
	if err != nil {
		t.Fatalf("OpenArtifactStoreFromSpec: %v", err)
	}

	payload := []byte("compiled pipeline bytes")
	src := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(src, payload, 0o755); err != nil {
		t.Fatal(err)
	}

	const key = "deadbeef-cafebabe"
	if err := bincache.UploadToArtifactStore(ctx, store, key, src); err != nil {
		t.Fatalf("UploadToArtifactStore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binsDir, key)); err != nil {
		t.Fatalf("blob did not land on the /bin/ route: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binsDir, key+".sha256")); err != nil {
		t.Fatalf("digest sidecar did not land on the /bin/ route: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binsDir, "bin")); err == nil {
		t.Errorf("the key's bin/ prefix reached the server as a path segment")
	}

	dest := filepath.Join(t.TempDir(), "pipeline")
	if err := bincache.FetchFromArtifactStore(ctx, store, key, dest); err != nil {
		t.Fatalf("FetchFromArtifactStore: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("fetched %q, want %q", got, payload)
	}
}
