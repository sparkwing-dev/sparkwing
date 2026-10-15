package depcacheblob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type remoteArchive struct {
	blob.RemoteCache
	body           []byte
	key            blob.DepcacheKey
	budget         blob.Budget
	err            error
	closeErr       error
	lookups, reads int
	corrupt        bool
	onLookup       func()
	onRead         func()
}

func (r *remoteArchive) Depcache(_ context.Context, key blob.DepcacheKey, budget blob.Budget) (blob.Record, error) {
	r.lookups++
	r.key, r.budget = key, budget
	if r.onLookup != nil {
		r.onLookup()
	}
	return blob.Record{Digest: blob.Digest(fmt.Sprintf("%x", sha256.Sum256(r.body)))}, r.err
}

func (r *remoteArchive) Artifact(ctx context.Context, d blob.Digest) (blob.VerifiedReader, error) {
	r.reads++
	if r.onRead != nil {
		r.onRead()
	}
	if d != blob.Digest(fmt.Sprintf("%x", sha256.Sum256(r.body))) {
		return nil, blob.ErrDigestNotReadable
	}
	data := bytes.Clone(r.body)
	if r.corrupt {
		data[len(data)-1] ^= 0xff
	}
	f := &fixture{data: data, intent: blob.UploadIntent{Digest: d, Size: int64(len(data))}, downloadCloseErr: r.closeErr}
	return blob.NewSignedStore(blob.SignedStoreOptions{Signer: f, HTTPClient: f}).Get(ctx, blob.GetRequest{Digest: d})
}

func TestRemoteDepcacheRestoresVerifiedArchive(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tool"), []byte("cached tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := depcache.WriteArchive(&archive, source); err != nil {
		t.Fatal(err)
	}
	remote := &remoteArchive{body: archive.Bytes()}
	budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
	reader, err := NewRemoteReader(remote, runspec.RepoKey("gh:123"), budget)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := reader.Exists(t.Context(), "deps"); err != nil || !found {
		t.Fatalf("probe = %v, %v", found, err)
	}
	if remote.key != (blob.DepcacheKey{Repo: "gh:123", Name: "deps", Key: "deps"}) || remote.budget != budget {
		t.Fatalf("lookup = %+v, %+v", remote.key, remote.budget)
	}
	dest := filepath.Join(t.TempDir(), "restore")
	if n, err := reader.Fetch(t.Context(), "deps", dest); err != nil || n != int64(archive.Len()) {
		t.Fatalf("restore = %d, %v", n, err)
	}
	if body, err := os.ReadFile(filepath.Join(dest, "tool")); err != nil || string(body) != "cached tool" {
		t.Fatalf("restored = %q, %v", body, err)
	}
	for _, failure := range []string{"lookup", "close", "corrupt", "budget", "cancel", "lookup_cancelled", "read_cancelled"} {
		t.Run(failure, func(t *testing.T) {
			r := &remoteArchive{body: archive.Bytes()}
			b := budget
			ctx := t.Context()
			switch failure {
			case "lookup":
				r.err = blob.ErrNotFound
			case "close":
				r.closeErr = errors.New("close failed")
			case "corrupt":
				r.corrupt = true
			case "budget":
				b.Deadline = time.Now().Add(-time.Minute)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "lookup_cancelled", "read_cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				if failure == "lookup_cancelled" {
					r.onLookup = cancel
				} else {
					r.onRead = cancel
				}
			}
			reader, err := NewRemoteReader(r, "gh:123", b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Fetch(ctx, "deps", dest); err == nil {
				t.Fatal("failed restore succeeded")
			}
			if body, err := os.ReadFile(filepath.Join(dest, "tool")); err != nil || string(body) != "cached tool" {
				t.Fatalf("failed restore changed destination: %q, %v", body, err)
			}
			if (failure == "budget" || failure == "cancel") && (r.lookups != 0 || r.reads != 0) {
				t.Fatal("expired restore used remote authority")
			}
		})
	}
}

func TestRemoteDepcacheProbePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cache := &remoteArchive{onLookup: cancel}
	reader, err := NewRemoteReader(cache, "gh:123", blob.Budget{Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := reader.Exists(ctx, "deps"); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe = %v, %v", found, err)
	}
}

func TestRemoteDepcacheRequiresExplicitBinding(t *testing.T) {
	for _, tc := range []struct {
		cache  blob.RemoteCache
		repo   runspec.RepoKey
		budget blob.Budget
	}{
		{nil, "gh:123", blob.Budget{Deadline: time.Now().Add(time.Minute)}},
		{&remoteArchive{}, "", blob.Budget{Deadline: time.Now().Add(time.Minute)}},
		{&remoteArchive{}, "gh:123", blob.Budget{}},
	} {
		if _, err := NewRemoteReader(tc.cache, tc.repo, tc.budget); err == nil {
			t.Fatal("incomplete binding accepted")
		}
	}
}
