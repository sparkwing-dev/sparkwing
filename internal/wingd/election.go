package wingd

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

var errPeerClosed = errors.New("wingd: peer closed connection")

func (d *Daemon) elect() (bool, error) {
	if err := d.layout.ensureDir(); err != nil {
		return false, err
	}
	f, err := fssecure.OpenFile(d.layout.lock, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return false, fmt.Errorf("wingd: open lock %s: %w", d.layout.lock, err)
	}
	ok, err := flockTry(f)
	if err != nil {
		_ = f.Close()
		return false, fmt.Errorf("wingd: flock %s: %w", d.layout.lock, err)
	}
	if !ok {
		_ = f.Close()
		return false, nil
	}
	d.lockFile = f
	return true, nil
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

func (d *Daemon) bindListener() (net.Listener, error) {
	return d.bind(d.layout.sock)
}

func (d *Daemon) bind(path string) (net.Listener, error) {
	if d.listen != nil {
		return d.listen(path)
	}
	return bindSocket(path)
}

type SocketPreparation uint8

const (
	SocketPreparationUnknown SocketPreparation = iota
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
		return SocketPreparationUnknown, err
	}
	if err := l.ensureDir(); err != nil {
		return SocketPreparationUnknown, err
	}
	f, err := fssecure.OpenFile(l.lock, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return SocketPreparationUnknown, err
	}
	defer func() { _ = f.Close() }()
	ok, err := flockTry(f)
	if err != nil {
		return SocketPreparationUnknown, err
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
