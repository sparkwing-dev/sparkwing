package wingd

import (
	"context"
	"net"
	"sync"
	"time"
)

// APIDrainWindow is how long a stopping daemon waits for the controller API
// requests in flight to finish. It nests inside
// [FinalizeDrainWindow] so the store handle the host closes when the daemon
// returns outlives both drains.
const APIDrainWindow = 5 * time.Second

// APIConn is a connection accepted on api.sock. The daemon checks the peer's
// uid before it yields the connection, so PeerUID names the account the
// kernel reported, or this daemon's own uid on a platform that reports none.
type APIConn interface {
	net.Conn
	PeerUID() int
}

func (d *Daemon) startAPI(ctx context.Context) {
	if d.cfg.ServeAPI == nil {
		return
	}
	ln, err := bindSocket(d.layout.apiSock)
	if err != nil {
		// safety: admission is the daemon's reason to exist, so a socket the
		// API cannot bind is reported through api_ready rather than taken as
		// a reason to leave the machine without a daemon.
		d.mu.Lock()
		d.apiErr = err.Error()
		d.mu.Unlock()
		d.cfg.logf("controller API unavailable: %v", err)
		return
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
		uid, perr := acceptedPeerUID(nc)
		if perr != nil {
			l.d.cfg.logf("%v", perr)
			_ = nc.Close()
			continue
		}
		l.d.apiConnOpened()
		return &apiConn{Conn: nc, d: l.d, uid: uid}, nil
	}
}

type apiConn struct {
	net.Conn
	d    *Daemon
	uid  int
	once sync.Once
}

func (c *apiConn) PeerUID() int { return c.uid }

func (c *apiConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.d.apiConnClosed)
	return err
}

func (d *Daemon) apiConnOpened() {
	d.mu.Lock()
	d.apiConns++
	d.touchLocked()
	d.mu.Unlock()
}

func (d *Daemon) apiConnClosed() {
	d.mu.Lock()
	if d.apiConns > 0 {
		d.apiConns--
	}
	d.touchLocked()
	d.mu.Unlock()
}
