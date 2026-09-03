package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func TestReadHTTPErrorMapsMaskedNotImplementedToStorageSentinel(t *testing.T) {
	resp := &http.Response{
		Status:     "501 Not Implemented",
		StatusCode: http.StatusNotImplemented,
		Body:       io.NopCloser(strings.NewReader(`{"error":"internal server error"}`)),
	}
	err := readHTTPError(resp)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("error = %v, want storage.ErrNotSupported", err)
	}
	if strings.Contains(err.Error(), "internal server error") {
		t.Fatalf("error copied masked response body: %v", err)
	}
}
