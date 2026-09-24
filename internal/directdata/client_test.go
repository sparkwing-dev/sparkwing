package directdata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadUsesTheSignedRouteWithoutAnUploadRunField(t *testing.T) {
	object := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "bytes")
	}))
	defer object.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request) != 2 || request["kind"] != "artifact" || request["key"] != "artifacts/blobs/hash" {
			t.Errorf("download request = %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"url": object.URL, "sha256": "hash", "size": 5})
	}))
	defer controller.Close()
	client := New(controller.URL, "grant", "run", nil)
	body, _, err := client.Download(context.Background(), "artifact", "artifacts/blobs/hash")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "bytes" {
		t.Fatalf("download = %q, %v", got, err)
	}
}

func TestPartialDirectCapabilitiesRefuseLegacyFallback(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"upload":true,"download":false}`)
	}))
	defer controller.Close()
	available, err := New(controller.URL, "grant", "run", nil).Available(context.Background())
	if available || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("partial capabilities = %v, %v", available, err)
	}
}
