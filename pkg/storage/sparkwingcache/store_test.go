package sparkwingcache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func TestStore_PutGetHasDelete(t *testing.T) {
	t.Parallel()

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
			w.Write(b)
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
	defer srv.Close()

	s := New(srv.URL, "tok", nil)
	ctx := context.Background()

	if err := s.Put(ctx, "k1", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Fatalf("Get body = %q, want hello", got)
	}

	has, err := s.Has(ctx, "k1")
	if err != nil || !has {
		t.Fatalf("Has(k1) = (%v, %v), want (true, nil)", has, err)
	}
	has, err = s.Has(ctx, "missing")
	if err != nil || has {
		t.Fatalf("Has(missing) = (%v, %v), want (false, nil)", has, err)
	}

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if has, _ := s.Has(ctx, "k1"); has {
		t.Fatalf("Has after Delete = true, want false")
	}
}
