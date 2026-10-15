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
	return copyPeerIdentity(peer), ok
}

// ServeAPI carries verified transport identity to the shared route authorizer.
func (s LocalHTTP) ServeAPI(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("wingd: controller HTTP requires a listener")
	}
	return ServeLocalHTTP(ctx, localAPIListener{listener}, s.API, func(ctx context.Context, connection net.Conn) context.Context {
		return context.WithValue(ctx, localPeerKey{}, connection.(APIConn).PeerIdentity())
	}, nil)
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
	observe  func(int)
}

func (l localHTTPConnections) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.observe != nil {
		l.observe(1)
	}
	return &localHTTPConn{Conn: connection, hijacked: l.hijacked, observe: l.observe}, nil
}

type localHTTPConn struct {
	net.Conn
	hijacked *sync.Map
	observe  func(int)
	once     sync.Once
	err      error
}

func (c *localHTTPConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.hijacked.Delete(c)
		if c.observe != nil {
			c.observe(-1)
		}
	})
	return c.err
}

func (c *localHTTPConn) PeerIdentity() LocalPeerIdentity {
	if peer, ok := c.Conn.(APIConn); ok {
		return peer.PeerIdentity()
	}
	return LocalPeerIdentity{}
}

// ServeLocalHTTP cancels and joins handlers and connections before returning.
func ServeLocalHTTP(ctx context.Context, listener net.Listener, handler http.Handler, connectionContext func(context.Context, net.Conn) context.Context, observe func(int)) error {
	if listener == nil || handler == nil {
		return errors.New("wingd: controller HTTP requires a listener and shared handler")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	shutdown := make(chan error, 1)
	go func() {
		<-ctx.Done()
		drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), APIDrainWindow)
		defer cancel()
		shutdown <- server.Shutdown(drain)
	}()
	err := server.Serve(localHTTPConnections{Listener: listener, hijacked: &hijacked, observe: observe})
	cancel()
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
