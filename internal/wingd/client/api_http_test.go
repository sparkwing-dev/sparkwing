package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestAPIClientPreservesResponsesWithoutReplayingWrites(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusConflict, http.StatusServiceUnavailable, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewAPIClient("unused")
			defer client.CloseIdleConnections()
			transport := client.Transport.(*http.Transport)
			var calls atomic.Int32
			var requests atomic.Int32
			transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				calls.Add(1)
				local, owner := net.Pipe()
				go func() {
					defer owner.Close()
					request, err := http.ReadRequest(bufio.NewReader(owner))
					if err != nil {
						t.Error(err)
						return
					}
					requests.Add(1)
					body, err := io.ReadAll(request.Body)
					request.Body.Close()
					if err != nil || string(body) != `{"kinds":["trigger","node"]}` || request.URL.Path != "/api/v1/runners/claim" {
						t.Errorf("changed request: %s %s: %v", request.URL.Path, body, err)
					}
					_, _ = fmt.Fprintf(owner, "HTTP/1.1 %d %s\r\nContent-Length: 2\r\nLocation: https://remote.invalid/claim\r\nConnection: close\r\n\r\n{}", status, http.StatusText(status))
				}()
				return local, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBaseURL+"/api/v1/runners/claim", strings.NewReader(`{"kinds":["trigger","node"]}`))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(body) != "{}" || response.StatusCode != status || calls.Load() != 1 || requests.Load() != 1 {
				t.Fatalf("response=%d body=%q dials=%d requests=%d error=%v", response.StatusCode, body, calls.Load(), requests.Load(), err)
			}
		})
	}
}

func FuzzAPITransportUsesPrivateSocket(f *testing.F) {
	f.Add("tcp", "proxy.invalid:8080")
	f.Add("tcp6", "[::1]:443")
	f.Fuzz(func(t *testing.T, network, address string) {
		parent := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(parent, "api.sock")
		client := NewAPIClient(socket)
		defer client.CloseIdleConnections()
		transport := client.Transport.(*http.Transport)
		if transport.Proxy != nil {
			t.Fatal("private transport consults a proxy")
		}
		if want := wingd.ValidateSocketDir(socket); want == nil {
			t.Fatal("invalid socket directory accepted")
		} else if connection, err := transport.DialContext(t.Context(), network, address); connection != nil || err == nil || err.Error() != want.Error() {
			if connection != nil {
				connection.Close()
			}
			t.Fatalf("request destination bypassed private socket validation: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		connection, err := transport.DialContext(ctx, network, address)
		if connection != nil {
			connection.Close()
			t.Fatal("cancelled private dial returned a connection")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("private socket dial did not receive cancellation: %v", err)
		}
		request, err := http.NewRequest(http.MethodGet, "https://remote.invalid/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(client.CheckRedirect(request, nil), http.ErrUseLastResponse) {
			t.Fatal("local client followed a redirect")
		}
	})
}
