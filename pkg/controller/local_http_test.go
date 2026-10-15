package controller

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type localControllerConn struct {
	net.Conn
	peer wingd.LocalPeerIdentity
}

func (c localControllerConn) PeerIdentity() wingd.LocalPeerIdentity { return c.peer }

type localControllerListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *localControllerListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *localControllerListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (*localControllerListener) Addr() net.Addr { return &net.UnixAddr{Name: "api.sock", Net: "unix"} }

func TestLocalControllerHTTPUsesSharedPolicy(t *testing.T) {
	t.Run("injected", func(t *testing.T) { testLocalControllerHTTP(t, false) })
	t.Run("native", func(t *testing.T) { testLocalControllerHTTP(t, true) })
}

func testLocalControllerHTTP(t *testing.T, native bool) {
	s := New(newStoreForAuth(t), nil)
	b := &peerBoundary{}
	if !native {
		s.WithAuthorization(RouteAuthorization{
			Authenticator: capability.NewAuthenticator(b), Bindings: b,
			Identify: func(r *http.Request) (capability.Presentation, error) {
				if len(r.CookiesNamed("__Host-sw_session")) > 0 {
					return capability.Presentation{}, sessionUnavailable()
				}
				return s.identifyPeer(r)
			},
		})
	}
	services := s.HTTP()
	s.HTTP()
	uid := uint32(1234)
	listener := &localControllerListener{connections: make(chan net.Conn), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- services.ServeAPI(ctx, listener) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case listener.connections <- localControllerConn{Conn: server, peer: wingd.LocalPeerIdentity{GOOS: "linux", UID: &uid}}:
			return client, nil
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, ctx.Err()
		}
	}}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("local HTTP shutdown: %v", err)
		}
	})
	client := &http.Client{Transport: transport}
	property := func(input []byte, mutation uint8) bool {
		r, err := http.NewRequestWithContext(t.Context(), "GET", "http://local/api/v1/auth/whoami", nil)
		if err != nil {
			return false
		}
		forged := "forged-" + hex.EncodeToString(input)
		r.Header.Set("X-Local-UID", forged)
		status := 401
		switch mutation % 5 {
		case 0:
			status = 200
		case 1:
			r.Header.Set("Authorization", "Session "+forged)
		case 2:
			r.Header.Set("Authorization", "Bearer "+forged)
		case 3:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: forged})
		case 4:
			r.Header.Set("Authorization", "malformed")
		}
		resp, err := client.Do(r)
		if err != nil {
			return false
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		return err == nil && resp.StatusCode == status && (status != 200 || strings.Contains(string(body), `"principal":"local:1234"`)) && !strings.Contains(string(body), forged)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/triggers", "/api/v1/runs/run/nodes/job/start", "/api/v1/concurrency/group/acquire"} {
		r, _ := http.NewRequestWithContext(t.Context(), "POST", "http://local"+path, nil)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("retired local route %s: HTTP %d", path, resp.StatusCode)
		}
	}
	for _, handler := range []http.Handler{services.API, services.Dashboard} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/auth/whoami", nil)
		r.Header.Set("X-Local-UID", "1234")
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unverified HTTP %d %s", w.Code, w.Body.String())
		}
	}
}
