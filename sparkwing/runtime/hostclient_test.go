package sdkserve

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestHostClientSpeaksToTheNetHTTPHost(t *testing.T) {
	large := `{"output":"` + strings.Repeat("x", 64<<10) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer delegated" || r.Header.Get("Content-Type") != "application/json" || string(body) != "null" {
			http.Error(w, `{"error":{"code":"unauthenticated","message":"bad request shape"}}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/node/v1/jobs/small/output":
			_, _ = io.WriteString(w, `{"output":1}`)
		case "/node/v1/jobs/chunked/output":
			// safety: net/http switches to chunked encoding once a handler writes past its buffer without a length.
			_, _ = io.WriteString(w, large)
		case "/node/v1/jobs/refused/output":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no output"}}`)
		case "/node/v1/jobs/slow/output":
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	s := &routeServices{binding: &nodeproto.ProcessBinding{Routes: nodeproto.ProcessRoutes{URL: server.URL, Bearer: "delegated"}}}
	if out, err := s.Output(t.Context(), "small"); err != nil || string(out) != "1" {
		t.Fatalf("content-length response = %s, %v", out, err)
	}
	if out, err := s.Output(t.Context(), "chunked"); err != nil || len(out) != 64<<10+2 {
		t.Fatalf("chunked response = %d bytes, %v", len(out), err)
	}
	if _, err := s.Output(t.Context(), "refused"); err == nil || !strings.Contains(err.Error(), "no output") {
		t.Fatalf("host refusal = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Output(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled long poll = %v", err)
	}
}

func TestHostClientBoundsResponses(t *testing.T) {
	for name, response := range map[string]string{
		"declared length": "HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\n",
		"chunks":          "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n6\r\n123456\r\n6\r\n123456\r\n0\r\n\r\n",
		"unframed":        "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n12345678901",
	} {
		t.Run(name, func(t *testing.T) {
			dial := func(context.Context, string) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					go func() { _, _ = io.Copy(io.Discard, server) }()
					_, _ = io.WriteString(server, response)
					_ = server.Close()
				}()
				return client, nil
			}
			if _, err := roundTrip(t.Context(), dial, "127.0.0.1:1", "GET", "/", "token", nil, 10); !errors.Is(err, errResponseLimit) {
				t.Fatalf("oversized response = %v", err)
			}
		})
	}
	if _, err := roundTrip(t.Context(), dialLoopback, "127.0.0.1:1", "GET", "/path\r\nInjected: header", "token", nil, 10); err == nil {
		t.Fatal("request line injection accepted")
	}
	if _, err := roundTrip(t.Context(), dialLoopback, "127.0.0.1:1", "GET", "/", "token\r\nInjected: header", nil, 10); err == nil {
		t.Fatal("bearer header injection accepted")
	}
}
