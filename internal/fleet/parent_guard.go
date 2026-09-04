package fleet

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCoordinatorProcessGone distinguishes loss of the initiating CLI from an
// ordinary run failure so the coordinator records cancellation.
var ErrCoordinatorProcessGone = errors.New("foreground coordinator process exited")

// ParentGuard owns a loopback socket whose kernel close survives skipped defers.
type ParentGuard struct {
	Address string
	Token   string

	server *http.Server
	done   chan struct{}
	once   sync.Once
	joined atomic.Bool
}

// StartParentGuard creates a loopback lifetime channel for one coordinator child.
func StartParentGuard() (*ParentGuard, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("create coordinator lifetime credential: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for coordinator lifetime: %w", err)
	}
	g := &ParentGuard{
		Address: "http://" + listener.Addr().String(),
		Token:   base64.RawURLEncoding.EncodeToString(secret),
		done:    make(chan struct{}),
	}
	g.server = &http.Server{Handler: http.HandlerFunc(g.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = g.server.Serve(listener) }()
	return g, nil
}

func (g *ParentGuard) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/lifetime" {
		http.NotFound(w, r)
		return
	}
	const bearer = "Bearer "
	authorization := r.Header.Get("Authorization")
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, bearer))
	if !strings.HasPrefix(authorization, bearer) || subtle.ConstantTimeCompare([]byte(provided), []byte(g.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !g.joined.CompareAndSwap(false, true) {
		http.Error(w, "coordinator child is already attached", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
	<-g.done
}

// Close tells the attached child that its initiating process is ending.
func (g *ParentGuard) Close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		close(g.done)
		_ = g.server.Close()
	})
}

// JoinParentGuard returns a context canceled when the initiating process dies.
func JoinParentGuard(parent context.Context, address, token string) (context.Context, func(), error) {
	if address == "" || token == "" {
		return nil, nil, errors.New("foreground coordinator lifetime handoff is incomplete")
	}
	if err := validateParentGuardAddress(address); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(parent, http.MethodGet, strings.TrimRight(address, "/")+"/lifetime", nil)
	if err != nil {
		return nil, nil, errors.New("connect foreground coordinator lifetime")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		client.CloseIdleConnections()
		return nil, nil, errors.New("foreground coordinator process is unavailable")
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		client.CloseIdleConnections()
		return nil, nil, fmt.Errorf("foreground coordinator lifetime refused: %s", resp.Status)
	}
	ctx, cancel := context.WithCancelCause(parent)
	stopped := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		cancel(ErrCoordinatorProcessGone)
		close(stopped)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = resp.Body.Close()
			client.CloseIdleConnections()
			<-stopped
		})
	}
	return ctx, stop, nil
}

func validateParentGuardAddress(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" {
		return errors.New("foreground coordinator lifetime address is not a loopback HTTP origin")
	}
	ip, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]"))
	if err != nil || !ip.IsLoopback() {
		return errors.New("foreground coordinator lifetime address is not a loopback HTTP origin")
	}
	return nil
}
