package wingd

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
)

type localHTTPListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *localHTTPListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *localHTTPListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*localHTTPListener) Addr() net.Addr { return &net.UnixAddr{Name: "api.sock", Net: "unix"} }

type localHTTPConnection struct {
	net.Conn
	peer LocalPeerIdentity
}

func (c localHTTPConnection) PeerIdentity() LocalPeerIdentity { return c.peer }

func TestLocalHTTPPropagatesVerifiedIdentityAndJoinsRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	uid := uint32(123)
	listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
	client, server := net.Pipe()
	defer client.Close()
	listener.connections <- localHTTPConnection{Conn: server, peer: LocalPeerIdentity{GOOS: "linux", UID: &uid}}
	entered, stopped := make(chan struct{}), make(chan struct{})
	services := LocalHTTP{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := LocalPeer(r.Context())
		if !ok || peer.GOOS != "linux" || peer.UID == nil || *peer.UID != uid {
			t.Error("request lost verified connection identity")
		}
		*peer.UID = 456
		if again, _ := LocalPeer(r.Context()); *again.UID != uid {
			t.Error("request changed connection identity")
		}
		close(entered)
		<-r.Context().Done()
		close(stopped)
	})}
	done := make(chan error, 1)
	go func() { done <- services.ServeAPI(ctx, listener) }()
	if _, err := io.WriteString(client, "GET /api/v1/runs HTTP/1.1\r\nHost: private\r\nX-Peer-UID: 999\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	readDone := make(chan error, 1)
	go func() {
		response, err := http.ReadResponse(bufio.NewReader(client), nil)
		if response != nil {
			response.Body.Close()
		}
		readDone <- err
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("HTTP service returned before its handler")
	}
}

func TestLocalHTTPRefusesUnverifiedConnections(t *testing.T) {
	listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
	client, server := net.Pipe()
	defer client.Close()
	listener.connections <- server
	services := LocalHTTP{API: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("served unverified connection") })}
	if err := services.ServeAPI(t.Context(), listener); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("unverified transport: %v", err)
	}
	if _, ok := LocalPeer(t.Context()); ok {
		t.Fatal("ambient context manufactured a peer")
	}
}

func TestLocalHTTPJoinsHijackedHandlers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		uid := uint32(123)
		listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
		client, server := net.Pipe()
		defer client.Close()
		listener.connections <- localHTTPConnection{Conn: server, peer: LocalPeerIdentity{GOOS: "linux", UID: &uid}}
		entered, exited := make(chan struct{}), make(chan struct{})
		services := LocalHTTP{API: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			close(entered)
			if _, err := connection.Read(make([]byte, 1)); err == nil {
				t.Error("hijacked connection survived shutdown")
			}
			close(exited)
		})}
		done := make(chan error, 1)
		go func() { done <- services.ServeAPI(ctx, listener) }()
		if _, err := io.WriteString(client, "GET /api/v1/runs HTTP/1.1\r\nHost: private\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		<-entered
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		select {
		case <-exited:
		default:
			t.Fatal("hijacked handler outlived its HTTP service")
		}
	})
}

func FuzzLocalHTTPIdentity(f *testing.F) {
	f.Add(uint32(1000), "local:0")
	f.Add(uint32(0), "X-Peer-SID: forged")
	f.Fuzz(func(t *testing.T, uid uint32, asserted string) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
		client, server := net.Pipe()
		defer client.Close()
		listener.connections <- localHTTPConnection{Conn: server, peer: LocalPeerIdentity{GOOS: "linux", UID: &uid}}
		services := LocalHTTP{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, ok := LocalPeer(r.Context())
			if !ok || peer.UID == nil || *peer.UID != uid || peer.SID != "" {
				t.Error("HTTP input selected the peer identity")
			}
			w.WriteHeader(http.StatusNoContent)
		})}
		done := make(chan error, 1)
		go func() { done <- services.ServeAPI(ctx, listener) }()
		if _, err := fmt.Fprintf(client, "GET /api/v1/runs HTTP/1.1\r\nHost: private\r\nX-Peer-Identity: %s\r\nConnection: close\r\n\r\n", base64.RawStdEncoding.EncodeToString([]byte(asserted))); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("status %d", response.StatusCode)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
