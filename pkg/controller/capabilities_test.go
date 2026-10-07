package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func newPlainServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := httptest.NewServer(controller.New(s, nil).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestReconcileHook_RunsBeforeReads(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	dir := t.TempDir()
	s, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var hookCalls atomic.Int32
	ctrl := controller.New(s, nil).
		WithReconcileHook(func(_ context.Context) error {
			hookCalls.Add(1)
			return nil
		})
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := hookCalls.Load(); got != 1 {
		t.Errorf("after list: hook calls=%d want 1", got)
	}

	if err := s.CreateRun(context.Background(), store.Run{ID: "run-reconciled", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	resp2, err := http.Get(srv.URL + "/api/v1/runs/run-reconciled")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got := hookCalls.Load(); got != 2 {
		t.Errorf("after get: hook calls=%d want 2", got)
	}
}

func TestReconcileHook_NoHookIsPassThrough(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	dir := t.TempDir()
	s, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := httptest.NewServer(controller.New(s, nil).Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
