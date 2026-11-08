//go:build windows

package wingd

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func TestWindowsLocalHTTPKeepsRequestAliveAcrossReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := ListenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- ServeAPI(ctx, listener, localhostwire.LocalHTTP{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				t.Error("request cancelled before client release")
			case <-release:
			}
			_, _ = io.WriteString(w, "alive")
		})})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return DialSocket(ctx, path, 0) }}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for range 3 {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://private/probe", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		release <- struct{}{}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "alive" {
			t.Fatalf("body=%q err=%v", body, err)
		}
	}
}
