package wingd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

var errPeerClosed = errors.New("wingd: peer closed connection")

func (d *Daemon) elect() (bool, error) {
	f, err := acquireProcessLock(d.layout)
	d.lockFile = f
	return f != nil, err
}

// AcquireControllerLock excludes other controllers and wingd for this home.
// Close the returned file only after the controller has stopped using its store.
func AcquireControllerLock(home string) (*os.File, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return nil, err
	}
	f, err := acquireProcessLock(l)
	if err == nil && f == nil {
		err = fmt.Errorf("another controller or wingd already owns store directory %s", home)
	}
	return f, err
}

func acquireProcessLock(l layout) (*os.File, error) {
	if err := l.ensureDir(); err != nil {
		return nil, err
	}
	f, err := fssecure.OpenFile(l.lock, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("wingd: open lock %s: %w", l.lock, err)
	}
	ok, err := flockTry(f)
	if err != nil || !ok {
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("wingd: flock %s: %w", l.lock, err)
		}
		return nil, nil
	}
	return f, nil
}

func (d *Daemon) releaseLock() {
	if d.lockFile == nil {
		return
	}
	_ = flockUnlock(d.lockFile)
	_ = d.lockFile.Close()
	d.lockFile = nil
}

func LockHeld(home string) (bool, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return false, err
	}
	f, err := fssecure.OpenFile(l.lock, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("wingd: open lock %s: %w", l.lock, err)
	}
	defer func() { _ = f.Close() }()
	ok, err := flockTry(f)
	if err != nil {
		return false, fmt.Errorf("wingd: flock %s: %w", l.lock, err)
	}
	if !ok {
		return true, nil
	}
	_ = flockUnlock(f)
	return false, nil
}

const maxSocketRotations = 3

func (d *Daemon) bindListener() (net.Listener, error) {
	for rotation := 0; ; rotation++ {
		ln, err := d.bind(d.layout.sock)
		if !errors.Is(err, errUnsafeSocketDir) || rotation == maxSocketRotations {
			return ln, err
		}
		if rerr := rotateSocketToken(d.layout.dir); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		moved, rerr := daemonLayout(d.layout.home)
		if rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		d.cfg.logf("socket directory %s is unsafe; moving to %s", filepath.Dir(d.layout.sock), filepath.Dir(moved.sock))
		d.layout.sock, d.layout.apiSock = moved.sock, moved.apiSock
	}
}

func (d *Daemon) bind(path string) (net.Listener, error) {
	if d.listen != nil {
		return d.listen(path)
	}
	return bindSocket(path)
}

type SocketPreparation uint8

const (
	socketPreparationUnknown SocketPreparation = iota
	SocketPreparationElectionHeld
	SocketPreparationReady
	SocketPreparationCleanupFailed
)

// ClaimDaemonStart serializes startup until the socket answers. The daemon
// acquires its own election lock after spawning, leaving a gap for duplicate starters.
func ClaimDaemonStart(home string) (release func(), claimed bool, err error) {
	l, err := daemonLayout(home)
	if err != nil {
		return nil, false, err
	}
	if err := l.ensureDir(); err != nil {
		return nil, false, err
	}
	f, err := fssecure.OpenFile(l.start, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, false, err
	}
	ok, err := flockTry(f)
	if err != nil || !ok {
		_ = f.Close()
		return nil, false, err
	}
	return func() {
		_ = f.Close()
	}, true, nil
}

func PrepareDaemonSocket(home string) (SocketPreparation, error) {
	l, err := daemonLayout(home)
	if err != nil {
		return socketPreparationUnknown, err
	}
	if err := l.ensureDir(); err != nil {
		return socketPreparationUnknown, err
	}
	f, err := fssecure.OpenFile(l.lock, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return socketPreparationUnknown, err
	}
	defer func() { _ = f.Close() }()
	ok, err := flockTry(f)
	if err != nil {
		return socketPreparationUnknown, err
	}
	if !ok {
		return SocketPreparationElectionHeld, nil
	}
	defer func() { _ = flockUnlock(f) }()
	// safety: holding the election lock proves no daemon serves this home, so a
	// socket left at the path is stale and must not divert clients.
	if err := os.Remove(l.sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SocketPreparationCleanupFailed, err
	}
	return SocketPreparationReady, nil
}
