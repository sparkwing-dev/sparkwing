package sparkwinglogs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func TestStore_RoundTrip(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	store := map[string][]byte{} // runID/nodeID -> bytes

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/logs/")
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			store[path] = append(store[path], body...)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			mu.Lock()
			defer mu.Unlock()
			if !strings.Contains(path, "/") { // ReadRun
				var buf []byte
				for k, v := range store {
					if strings.HasPrefix(k, path+"/") {
						buf = append(buf, []byte("=== "+k+" ===\n")...)
						buf = append(buf, v...)
					}
				}
				w.Write(buf)
				return
			}
			grep := r.URL.Query().Get("grep")
			body := store[path]
			if grep != "" {
				out := []byte{}
				for _, line := range strings.Split(string(body), "\n") {
					if strings.Contains(line, grep) {
						out = append(out, []byte(line+"\n")...)
					}
				}
				body = out
			}
			w.Write(body)
		case http.MethodDelete:
			mu.Lock()
			for k := range store {
				if strings.HasPrefix(k, path+"/") || k == path {
					delete(store, k)
				}
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	var ls storage.LogStore = New(srv.URL, nil, "")
	ctx := context.Background()

	if err := ls.Append(ctx, "run1", "n1", []byte("hello\nworld\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ls.Append(ctx, "run1", "n2", []byte("alpha\nbeta\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ls.Read(ctx, "run1", "n1", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "hello\nworld\n" {
		t.Fatalf("Read = %q, want hello\\nworld\\n", got)
	}

	got, err = ls.Read(ctx, "run1", "n1", storage.ReadOpts{Grep: "world"})
	if err != nil {
		t.Fatalf("Read filtered: %v", err)
	}
	if !strings.Contains(string(got), "world") || strings.Contains(string(got), "hello") {
		t.Fatalf("Read filtered = %q, want only world", got)
	}

	got, err = ls.ReadRun(ctx, "run1")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if !strings.Contains(string(got), "alpha") || !strings.Contains(string(got), "hello") {
		t.Fatalf("ReadRun = %q, want both nodes", got)
	}

	if err := ls.DeleteRun(ctx, "run1"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	got, _ = ls.ReadRun(ctx, "run1")
	if len(got) != 0 {
		t.Fatalf("ReadRun after delete = %q, want empty", got)
	}
}
