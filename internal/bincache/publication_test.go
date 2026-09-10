package bincache_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

type failedPutStore struct {
	storage.ArtifactStore
	fail string
}

func (s failedPutStore) Put(ctx context.Context, key string, r io.Reader) error {
	if key == s.fail {
		return errors.New("interrupted upload")
	}
	return s.ArtifactStore.Put(ctx, key, r)
}

func TestInterruptedUploadDoesNotPublishBlobWithoutDigest(t *testing.T) {
	for _, failedKey := range []string{"bin/key.sha256", "bin/key"} {
		t.Run(failedKey, func(t *testing.T) {
			store, err := fs.NewArtifactStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(src, []byte("binary contents"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := bincache.UploadToArtifactStore(t.Context(), failedPutStore{store, failedKey}, "key", src); err == nil {
				t.Fatal("expected interrupted upload")
			}
			if exists, err := store.Has(t.Context(), "bin/key"); err != nil || exists {
				t.Fatalf("interrupted upload published blob: exists=%v err=%v", exists, err)
			}
			if err := bincache.UploadToArtifactStore(t.Context(), store, "key", src); err != nil {
				t.Fatal(err)
			}
			if err := bincache.FetchFromArtifactStore(t.Context(), store, "key", filepath.Join(t.TempDir(), "fetched")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type gatedDownload struct {
	reader           *strings.Reader
	entered, release chan struct{}
	first            bool
}

func (r *gatedDownload) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		close(r.entered)
		<-r.release
	}
	return r.reader.Read(p)
}

type downloadStore struct {
	storage.ArtifactStore
	body   *gatedDownload
	digest string
}

func (s downloadStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if strings.HasSuffix(key, ".sha256") {
		return io.NopCloser(strings.NewReader(s.digest)), nil
	}
	return io.NopCloser(s.body), nil
}

func TestConcurrentArtifactDownloadsHaveIndependentStaging(t *testing.T) {
	const payload = "complete binary"
	sum := sha256.Sum256([]byte(payload))
	dest := filepath.Join(t.TempDir(), "binary")
	makeBody := func() *gatedDownload {
		return &gatedDownload{reader: strings.NewReader(payload), entered: make(chan struct{}), release: make(chan struct{})}
	}
	first, second := makeBody(), makeBody()
	run := func(body *gatedDownload) <-chan error {
		done := make(chan error, 1)
		go func() {
			done <- bincache.FetchFromArtifactStore(t.Context(), downloadStore{body: body, digest: hex.EncodeToString(sum[:])}, "key", dest)
		}()
		return done
	}
	a := run(first)
	<-first.entered
	b := run(second)
	<-second.entered
	close(first.release)
	aerr := <-a
	close(second.release)
	berr := <-b
	if aerr != nil || berr != nil {
		t.Fatalf("concurrent fetches: first=%v second=%v", aerr, berr)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != payload {
		t.Fatalf("downloaded %q: %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging files remain: %v %v", entries, err)
	}
}

func TestFailedBinaryPublicationRemovesStaging(t *testing.T) {
	const payload = "complete binary"
	sum := sha256.Sum256([]byte(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(sum[:]))
		_, err := io.WriteString(w, payload)
		if err != nil {
			return
		}
	}))
	defer server.Close()
	for _, source := range []string{"HTTP", "artifact"} {
		t.Run(source, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "binary")
			if err := os.Mkdir(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			var err error
			if source == "HTTP" {
				err = bincache.TryBinary(t.Context(), server.URL, "", "key", dest)
			} else {
				release := make(chan struct{})
				close(release)
				body := &gatedDownload{reader: strings.NewReader(payload), entered: make(chan struct{}), release: release}
				err = bincache.FetchFromArtifactStore(t.Context(), downloadStore{body: body, digest: hex.EncodeToString(sum[:])}, "key", dest)
			}
			if err == nil {
				t.Fatal("published over destination directory")
			}
			entries, readErr := os.ReadDir(parent)
			if readErr != nil || len(entries) != 1 || entries[0].Name() != "binary" {
				t.Fatalf("staging remains after publication failure: %v %v", entries, readErr)
			}
		})
	}
}
