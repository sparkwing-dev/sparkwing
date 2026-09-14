package controller

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestPoolBinding_ServesRequestsWhileTheBindingIsBuilt(t *testing.T) {
	client := fake.NewSimpleClientset()
	// safety: the config read parks here, so every request in the first round
	// lands while the binding is genuinely half-built.
	release := make(chan struct{})
	client.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})

	s := (&Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).AttachPool(PoolConfig{
		Client:         client,
		Namespace:      "sparkwing",
		ReconcileEvery: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		s.pool.run(ctx, s.logger)
	}()

	hammerPoolList(t, s)
	close(release)
	hammerPoolList(t, s)

	cancel()
	<-ran

	rec := httptest.NewRecorder()
	s.handlePoolList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pool", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pool list once the binding is built = %d, want 200", rec.Code)
	}
}

func hammerPoolList(t *testing.T, s *Server) {
	t.Helper()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				rec := httptest.NewRecorder()
				s.handlePoolList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pool", nil))
				if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
					t.Errorf("pool list during startup = %d, want 200 or 503", rec.Code)
					return
				}
			}
		}()
	}
	wg.Wait()
}
