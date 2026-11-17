package wingd

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

const privateControllerIdle = 20 * time.Minute

type idleClient struct{ background bool }
type idleConnectionKey struct{}

func (r *localRuntime) privateIdleExit(ctx context.Context, session *localRuntimeSession) bool {
	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(r.layout.home) == filepath.Join(home, ".sparkwing") || session.standalone {
		return false
	}
	if r.cfg.Installed != nil {
		installed, err := r.cfg.Installed(ctx)
		if err != nil {
			session.daemon.cfg.logf("cannot inspect installed controller for %s; idle exit disabled: %v", r.layout.home, err)
			return false
		}
		if installed {
			return false
		}
	}
	session.daemon.mu.Lock()
	session.daemon.idleSince = time.Now()
	session.daemon.mu.Unlock()
	return true
}

func (d *Daemon) watchPrivateIdle(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		d.mu.Lock()
		busy := d.privateIdleBusyLocked()
		if busy {
			d.idleSince = time.Now()
		}
		expired := !busy && time.Since(d.idleSince) >= privateControllerIdle && !d.draining && !d.shuttingDown
		d.mu.Unlock()
		if !expired {
			continue
		}
		if source, ok := d.cfg.Runs.(interface {
			LocalIdleActivity(context.Context) (LocalActivity, error)
		}); ok {
			activity, err := source.LocalIdleActivity(ctx)
			if err != nil || activity != (LocalActivity{}) {
				d.mu.Lock()
				d.idleSince = time.Now()
				d.mu.Unlock()
				continue
			}
		}
		d.mu.Lock()
		// safety: requests and activity reports can arrive while the store snapshot is read.
		if d.privateIdleBusyLocked() || time.Since(d.idleSince) < privateControllerIdle {
			d.mu.Unlock()
			continue
		}
		d.draining = true
		d.idleRetiring = true
		d.mu.Unlock()
		if d.cfg.Logf != nil {
			d.cfg.logf("private controller idle exit: home=%s idle=%s", d.layout.home, privateControllerIdle)
		} else {
			slog.Info("private controller idle exit", "home", d.layout.home, "idle", privateControllerIdle)
		}
		for {
			if err := d.retireLocalRunner(ctx); err != nil {
				var held *runnerHoldsWork
				if errors.As(err, &held) {
					d.finishDrainAfterRunner(context.WithoutCancel(ctx), held.instance)
					return
				}
				d.cfg.logf("private controller runner retirement failed: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					continue
				}
			}
			break
		}
		d.shutdownWithReason("private home idle")
		return
	}
}

func idleRunnerRequest(path string) bool {
	return path == "/api/v1/runners/claim" || path == controllerHTTPPath+"probe" || path == localRunnerPath || strings.HasPrefix(path, localRunnerPath+"/")
}

func (d *Daemon) serveIdleAPI(ctx context.Context, listener net.Listener, local localhostwire.LocalHTTP) error {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, _ := r.Context().Value(idleConnectionKey{}).(net.Conn)
		background := idleRunnerRequest(r.URL.Path)
		d.mu.Lock()
		client := d.idleClients[connection]
		client.background = background
		d.idleClients[connection] = client
		if !background {
			d.idleSince = time.Now()
		}
		retiring := d.idleRetiring
		d.mu.Unlock()
		if retiring && r.URL.Path == "/api/v1/runners/claim" {
			writeLocalRunnerFailure(w, errControllerStopping)
			return
		}
		local.API.ServeHTTP(w, r)
	})
	return serveLocalHTTP(ctx, localAPIListener{listener}, handler, func(ctx context.Context, connection net.Conn) context.Context {
		ctx = localhostwire.WithLocalSocketPeer(ctx, connection.(authenticatedAPIConn).PeerIdentity())
		return context.WithValue(context.WithValue(ctx, socketPeerContextKey{}, true), idleConnectionKey{}, connection)
	}, nil, 0, func(connection net.Conn, state http.ConnState) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.idleClients == nil {
			d.idleClients = make(map[net.Conn]idleClient)
		}
		switch state {
		case http.StateNew:
			d.idleClients[connection] = idleClient{}
		case http.StateClosed, http.StateHijacked:
			if client := d.idleClients[connection]; !client.background {
				d.idleSince = time.Now()
			}
			delete(d.idleClients, connection)
		}
	})
}

func (d *Daemon) privateIdleBusyLocked() bool {
	if d.localActivity != (LocalActivity{}) || d.dashboardConns > 0 || len(d.conns) > 0 {
		return true
	}
	for _, client := range d.idleClients {
		if !client.background {
			return true
		}
	}
	return false
}
