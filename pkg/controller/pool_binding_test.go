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
	client.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		time.Sleep(50 * time.Millisecond)
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

	var wg sync.WaitGroup
	deadline := time.Now().Add(200 * time.Millisecond)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
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

	cancel()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("pool run did not return after cancellation")
	}
}
