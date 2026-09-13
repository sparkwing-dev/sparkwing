package controller

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackNamesARouteItDoesNotRegister(t *testing.T) {
	const token = "swl_unsupported"
	lb := NewLoopback(&leaseRecorderState{}, "run-loopback", token,
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	srv := httptest.NewServer(lb.Handler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs/run-loopback/receipt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET receipt: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
		Route string `json:"route"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != UnsupportedRouteError {
		t.Fatalf("error = %q, want %q; a client reads a bodyless 404 as a transport failure", body.Error, UnsupportedRouteError)
	}
	if want := "GET /api/v1/runs/run-loopback/receipt"; body.Route != want {
		t.Fatalf("route = %q, want %q", body.Route, want)
	}
}
