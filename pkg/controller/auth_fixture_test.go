package controller

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func teapotHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

func newStoreForAuth(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := teststore.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
