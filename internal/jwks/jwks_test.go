package jwks_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/jwks"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestKeySetServesCachedKeysWhileARefetchIsInFlight(t *testing.T) {
	known, rotated := jwkstest.NewSigner(t, "known"), jwkstest.NewSigner(t, "rotated")
	var fetches atomic.Int32
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fetches.Add(1) > 1 {
			entered <- struct{}{}
			<-release
			rotated.ServeKeys(w, r)
			return
		}
		known.ServeKeys(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	c := &clock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	keys := jwks.New(server.URL, nil, c.Now)
	good, err := known.Sign(map[string]any{"sub": "a"})
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := keys.Verify(t.Context(), good, &claims); err != nil {
		t.Fatal(err)
	}
	c.Advance(2 * time.Minute)
	fresh, err := rotated.Sign(map[string]any{"sub": "b"})
	if err != nil {
		t.Fatal(err)
	}
	var waiters sync.WaitGroup
	results := make(chan error, 8)
	for range cap(results) {
		waiters.Go(func() {
			var claims map[string]any
			results <- keys.Verify(t.Context(), fresh, &claims)
		})
	}
	<-entered
	done := make(chan error, 1)
	go func() {
		var claims map[string]any
		done <- keys.Verify(t.Context(), good, &claims)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cached key during refetch = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cached key waited on the provider fetch")
	}
	close(release)
	waiters.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("rotated key = %v", err)
		}
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("concurrent unknown keys fetched %d times", n)
	}
}

func TestKeySetBacksOffAfterAFailedFetch(t *testing.T) {
	signer := jwkstest.NewSigner(t, "k")
	var fetches atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if !healthy.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		signer.ServeKeys(w, r)
	}))
	t.Cleanup(server.Close)
	c := &clock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	keys := jwks.New(server.URL, nil, c.Now)
	token, err := signer.Sign(map[string]any{"sub": "a"})
	if err != nil {
		t.Fatal(err)
	}
	verify := func() error {
		var claims map[string]any
		return keys.Verify(t.Context(), token, &claims)
	}
	for range 5 {
		if err := verify(); err == nil || errors.Is(err, jwks.ErrRejected) {
			t.Fatalf("unavailable provider = %v", err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("failed provider fetched %d times without backing off", n)
	}
	healthy.Store(true)
	c.Advance(time.Minute)
	if err := verify(); err != nil {
		t.Fatalf("after backoff = %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches after backoff = %d", n)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var claims map[string]any
	if err := keys.Verify(ctx, token, &claims); err != nil {
		t.Fatalf("cached key with a canceled request = %v", err)
	}
}
