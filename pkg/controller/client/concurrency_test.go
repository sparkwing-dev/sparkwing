package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestObserveSlotMapsMissingHolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client()).ObserveSlot(context.Background(), "group", "holder")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ObserveSlot error = %v, want %v", err, store.ErrNotFound)
	}
}
