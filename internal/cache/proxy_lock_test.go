package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func proxyLockCount() int {
	proxyKeyLocksMu.Lock()
	defer proxyKeyLocksMu.Unlock()
	return len(proxyKeyLocks)
}

func TestProxyFailedRequestsReleaseCoordination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	withTestProxy(t, map[string]Registry{"test": {Name: "test", Upstream: upstream.URL}}, func() {
		before := proxyLockCount()
		for i := range 64 {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/proxy/test/missing-%d", i/4), nil)
			req.Header.Set("Accept", fmt.Sprintf("application/test-%d", i%4))
			out := httptest.NewRecorder()
			handleProxy(out, req)
			if out.Code != http.StatusNotFound {
				t.Fatalf("response=%d", out.Code)
			}
		}
		if got := proxyLockCount(); got != before {
			t.Fatalf("inactive coordination entries: got %d want %d", got, before)
		}
	})
}

func TestProxyCanceledFetchReleasesCoordination(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	withTestProxy(t, map[string]Registry{"test": {Name: "test", Upstream: upstream.URL}}, func() {
		before := proxyLockCount()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/proxy/test/canceled", nil).WithContext(ctx)
		out := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { handleProxy(out, req); close(done) }()
		<-entered
		cancel()
		<-done
		if out.Code != http.StatusBadGateway {
			t.Fatalf("response=%d", out.Code)
		}
		if got := proxyLockCount(); got != before {
			t.Fatalf("canceled coordination entries: got %d want %d", got, before)
		}
	})
}

func TestProxyCoordinationRetainsCallersBeforeTheyLock(t *testing.T) {
	before := proxyLockCount()
	first, releaseFirst := proxyKeyLock("waiting-writer")
	first.RLock()
	writer, releaseWriter := proxyKeyLock("waiting-writer")
	first.RUnlock()
	releaseFirst()
	reader, releaseReader := proxyKeyLock("waiting-writer")
	if writer != reader {
		t.Fatal("waiting caller lost its shared mutex")
	}
	published := false
	writer.Lock()
	published = true
	if reader.TryRLock() {
		reader.RUnlock()
		writer.Unlock()
		releaseWriter()
		t.Fatal("new caller acquired a second mutex while writer held the key")
	}
	writer.Unlock()
	releaseWriter()
	reader.RLock()
	if !published {
		t.Error("reader did not observe the writer publication")
	}
	reader.RUnlock()
	releaseReader()
	if got := proxyLockCount(); got != before {
		t.Fatalf("completed key retained: got %d want %d", got, before)
	}
}
