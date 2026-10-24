package wingd

import (
	"context"
	"net"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

// apiDrainWindow bounds the API request drain before the finalization drain.
const apiDrainWindow = 5 * time.Second

// authenticatedAPIConn carries a verified transport identity for the shared controller's
// local authorization. PeerIdentity returns a snapshot owned by the caller.
// Missing kernel UID or client SID evidence refuses access before Accept returns.
type authenticatedAPIConn interface {
	net.Conn
	PeerIdentity() localhostwire.LocalPeerIdentity
}

func (d *Daemon) startAPI(ctx context.Context) error {
	if d.cfg.ServeAPI == nil {
		return nil
	}
	ln, err := d.bind(d.layout.apiSock)
	if err != nil {
		d.mu.Lock()
		d.apiErr = err.Error()
		d.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	d.mu.Lock()
	d.apiLn = ln
	d.apiDone = done
	d.mu.Unlock()
	go func() {
		defer close(done)
		d.cfg.ServeAPI(ctx, apiListener{Listener: ln, d: d})
	}()
	d.cfg.logf("serving the controller API on %s", d.layout.apiSock)
	return nil
}

func (d *Daemon) apiState() (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.apiLn != nil {
		return true, ""
	}
	if d.apiErr != "" {
		return false, d.apiErr
	}
	return false, "the daemon is draining, so it no longer serves the controller API"
}

// safety: a successor binds api.sock as soon as it is elected, so the
// predecessor stops serving before it acknowledges a drain rather than when
// it finally exits.
func (d *Daemon) closeAPIListener() {
	d.mu.Lock()
	ln := d.apiLn
	d.apiLn = nil
	d.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (d *Daemon) awaitAPI(within time.Duration) {
	d.mu.Lock()
	done := d.apiDone
	d.mu.Unlock()
	if done == nil {
		return
	}
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		d.cfg.logf("shutdown: a controller API request did not finish within %s", within)
	}
}

type apiListener struct {
	net.Listener
	d *Daemon
}

func (l apiListener) Accept() (net.Conn, error) {
	for {
		nc, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peer, perr := acceptedPeerIdentity(nc)
		var connection *apiConn
		if perr == nil {
			var owner localhostwire.LocalPeerIdentity
			owner, perr = controllerPeerIdentity()
			if perr == nil {
				connection, perr = newAPIConn(nc, peer, owner)
			}
		}
		if perr != nil {
			l.d.cfg.logf("%v", perr)
			_ = nc.Close()
			continue
		}
		return connection, nil
	}
}

type apiConn struct {
	net.Conn
	identity localhostwire.LocalPeerIdentity
}

func newAPIConn(nc net.Conn, peer, owner localhostwire.LocalPeerIdentity) (*apiConn, error) {
	if _, err := LocalSubject(peer, owner); err != nil {
		return nil, err
	}
	return &apiConn{Conn: nc, identity: peer.Clone()}, nil
}

func (c *apiConn) PeerIdentity() localhostwire.LocalPeerIdentity { return c.identity.Clone() }
