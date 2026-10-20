package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A second controller on the same store stands by instead of serving beside
// the first, and takes over once the first lets go.
func TestServeLeaseKeepsOneControllerServing(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	first, err := acquireServeLease(t.Context(), st, "first", logger)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := acquireServeLease(waiting, st, "second", logger); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second controller = %v, want it standing by", err)
	}
	if !bytes.Contains(logs.Bytes(), []byte("standing by")) {
		t.Fatalf("stand-by not logged: %s", logs.String())
	}
	if err := st.AsOperator().ReleaseSingletonLease(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireServeLease(t.Context(), st, "second", logger); err != nil {
		t.Fatalf("second controller after release: %v", err)
	}
}

// A renewal that never returns, such as a stalled database connection, must
// not let the controller serve past its lease: the listener stops admitting
// at expiry, and the stalled renewal is bounded by the same deadline.
func TestServeLeaseExpiryStopsAStalledController(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	const ttl = 600 * time.Millisecond
	held := time.Now()
	renewDeadline := make(chan time.Time, 1)
	unstall := make(chan struct{})
	t.Cleanup(func() { close(unstall) })
	stall := func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		renewDeadline <- deadline
		<-unstall
		return ctx.Err()
	}
	serve := func(ctx context.Context) error {
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), ReadHeaderTimeout: time.Second}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- serveWhileLeased(t.Context(), held, ttl, stall, serve) }()

	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("leased controller refused a request: %v", err)
	}
	_ = resp.Body.Close()
	if deadline := <-renewDeadline; deadline.After(held.Add(ttl)) {
		t.Fatalf("renewal deadline %s runs past the lease's %s", deadline, held.Add(ttl))
	}
	select {
	case err := <-done:
		if !errors.Is(err, errServeLeaseExpired) {
			t.Fatalf("serving ended with %v, want the lease expiry", err)
		}
	case <-time.After(ttl + 2*time.Second):
		t.Fatal("controller kept serving after its lease expired while renewal stalled")
	}
	if elapsed := time.Since(held); elapsed < ttl {
		t.Fatalf("serving stopped after %s, before the lease expired", elapsed)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("listener still admits connections after the lease expired")
	}
}

// Transfer URLs a runner holds survive a controller restart.
func TestDataSigningKeyIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-signing.key")
	first, err := dataSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := dataSigningKey(path)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatalf("restart key differs: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file = %v, %v; want mode 0600", info, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dataSigningKey(path); err == nil {
		t.Fatal("a truncated signing key was accepted")
	}
}
