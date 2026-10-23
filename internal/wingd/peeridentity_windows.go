package wingd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"golang.org/x/sys/windows"
)

var impersonatePipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

func controllerPeerIdentity() (localhostwire.LocalPeerIdentity, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, fmt.Errorf("wingd: read controller SID: %w", err)
	}
	return localhostwire.LocalPeerIdentity{GOOS: "windows", SID: user.User.Sid.String()}, nil
}

func acceptedPeerIdentity(nc net.Conn) (localhostwire.LocalPeerIdentity, error) {
	c, ok := nc.(*pipeConn)
	if !ok || !c.server {
		return localhostwire.LocalPeerIdentity{}, errors.New("wingd: peer identity unavailable; local API access refused")
	}
	c.peerOnce.Do(func() {
		if !c.listener.startAuthentication() {
			c.peerErr = net.ErrClosed
			c.Close()
			return
		}
		defer c.listener.wait.Done()
		defer c.stopAuth()
		c.identity, c.peerErr = c.authenticateClient()
		if c.peerErr != nil {
			c.Close()
		}
	})
	return c.identity, c.peerErr
}

func (c *pipeConn) authenticateClient() (localhostwire.LocalPeerIdentity, error) {
	if err := c.SetDeadline(time.Now().Add(limits.LocalControllerHandshake)); err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	var preamble [1]byte
	if _, err := io.ReadFull(c, preamble[:]); err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	if preamble[0] != 0 {
		return localhostwire.LocalPeerIdentity{}, errors.New("wingd: invalid local pipe authentication preamble")
	}
	peer, err := c.clientIdentity()
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	owner, err := controllerPeerIdentity()
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	if _, err := LocalSubject(peer, owner); err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	if _, err := c.Write([]byte{0}); err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	return peer, nil
}

func (c *pipeConn) clientIdentity() (localhostwire.LocalPeerIdentity, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	var peer localhostwire.LocalPeerIdentity
	var inner error
	err = raw.Control(func(handle uintptr) { peer, inner = pipeClientIdentity(windows.Handle(handle)) })
	return peer, errors.Join(err, inner)
}

func pipeClientIdentity(handle windows.Handle) (localhostwire.LocalPeerIdentity, error) {
	type result struct {
		peer localhostwire.LocalPeerIdentity
		err  error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		var reply result
		defer func() { done <- reply }()
		ok, _, err := impersonatePipeClient.Call(uintptr(handle))
		if ok == 0 {
			runtime.UnlockOSThread()
			reply.err = fmt.Errorf("wingd: authenticate pipe client: %w", err)
			return
		}
		// safety: a thread that cannot revert must exit with this goroutine, never rejoin the scheduler.
		defer func() {
			if err := windows.RevertToSelf(); err != nil {
				reply.peer = localhostwire.LocalPeerIdentity{}
				reply.err = errors.Join(reply.err, fmt.Errorf("wingd: revert pipe client identity: %w", err))
				return
			}
			runtime.UnlockOSThread()
		}()
		var token windows.Token
		if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
			reply.err = err
			return
		}
		defer token.Close()
		user, err := token.GetTokenUser()
		if err != nil {
			reply.err = err
			return
		}
		reply.peer = localhostwire.LocalPeerIdentity{GOOS: "windows", SID: user.User.Sid.String()}
	}()
	reply := <-done
	return reply.peer, reply.err
}
