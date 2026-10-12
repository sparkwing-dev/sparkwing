package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// StreamAttempt names its attempt to the controller, and an attempt with
// no log yet reads as not found rather than as a failure.
func TestStreamAttemptNamesTheAttemptAndReportsNoLogAsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("attempt") != "2" {
			http.Error(w, `{"error":"no log for that attempt"}`, http.StatusNotFound)
			return
		}
		if q.Get("generation") != "3" {
			t.Errorf("generation = %q, want 3", q.Get("generation"))
		}
		_, _ = io.WriteString(w, "data: hello\n\n")
	}))
	t.Cleanup(srv.Close)
	logs := client.NewWithToken(srv.URL, nil, "").NodeLogs()

	if _, err := logs.StreamAttempt(context.Background(), "run-1", "a", 1, 3); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("attempt with no log = %v, want not found", err)
	}
	body, err := logs.StreamAttempt(context.Background(), "run-1", "a", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if got, _ := io.ReadAll(body); string(got) != "data: hello\n\n" {
		t.Fatalf("stream = %q", got)
	}
}
