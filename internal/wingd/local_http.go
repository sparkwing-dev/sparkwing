package wingd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
)

// LocalHTTP serves the shared controller handlers inside the elected local runtime.
type LocalHTTP struct {
	API, Dashboard http.Handler
}

type localPeerKey struct{}

// LocalPeer reads the verified api.sock connection identity from a request context.
func LocalPeer(ctx context.Context) (LocalPeerIdentity, bool) {
	peer, ok := ctx.Value(localPeerKey{}).(LocalPeerIdentity)
	if peer.UID != nil {
		uid := *peer.UID
		peer.UID = &uid
	}
	return peer, ok
}

// ServeAPI carries verified transport identity to the shared route authorizer.
func (s LocalHTTP) ServeAPI(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("wingd: controller HTTP requires a listener")
	}
	return serveLocalHTTP(ctx, localAPIListener{listener}, s.API, func(ctx context.Context, connection net.Conn) context.Context {
		return context.WithValue(ctx, localPeerKey{}, connection.(APIConn).PeerIdentity())
	})
}

// ServeDashboard retains the dashboard handler's browser authentication.
func (s LocalHTTP) ServeDashboard(ctx context.Context, listener net.Listener) error {
	return serveLocalHTTP(ctx, listener, s.Dashboard, nil)
}

type localAPIListener struct{ net.Listener }

func (l localAPIListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	peer, ok := connection.(APIConn)
	if ok {
		identity := peer.PeerIdentity()
		_, err = LocalSubject(identity, identity)
	}
	if !ok || err != nil {
		connection.Close()
		return nil, errors.New("wingd: controller HTTP requires a verified local peer")
	}
	return connection, nil
}

type localHTTPConnections struct {
	net.Listener
	hijacked *sync.Map
}

func (l localHTTPConnections) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &localHTTPConn{Conn: connection, hijacked: l.hijacked}, nil
}

type localHTTPConn struct {
	net.Conn
	hijacked *sync.Map
}

func (c *localHTTPConn) Close() error {
	c.hijacked.Delete(c)
	return c.Conn.Close()
}

func (c *localHTTPConn) PeerIdentity() LocalPeerIdentity {
	if peer, ok := c.Conn.(APIConn); ok {
		return peer.PeerIdentity()
	}
	return LocalPeerIdentity{}
}

func serveLocalHTTP(ctx context.Context, listener net.Listener, handler http.Handler, connectionContext func(context.Context, net.Conn) context.Context) error {
	if listener == nil || handler == nil {
		return errors.New("wingd: controller HTTP requires a listener and shared handler")
	}
	var connections, handlers sync.WaitGroup
	var hijacked sync.Map
	server := &http.Server{
		BaseContext: func(net.Listener) context.Context { return ctx },
		ConnContext: connectionContext,
		ConnState: func(connection net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				connections.Add(1)
			case http.StateClosed:
				connections.Done()
			case http.StateHijacked:
				hijacked.Store(connection, struct{}{})
				connections.Done()
			}
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlers.Add(1)
			defer handlers.Done()
			handler.ServeHTTP(w, r)
		}),
	}
	served, shutdown := make(chan struct{}), make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), APIDrainWindow)
			defer cancel()
			shutdown <- server.Shutdown(drain)
		case <-served:
			shutdown <- nil
		}
	}()
	err := server.Serve(localHTTPConnections{Listener: listener, hijacked: &hijacked})
	close(served)
	drainErr := <-shutdown
	closeErr := server.Close()
	connections.Wait()
	// safety: http.Server leaves hijacked connections open during shutdown.
	hijacked.Range(func(connection, _ any) bool { connection.(net.Conn).Close(); return true })
	handlers.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, drainErr, closeErr)
}
