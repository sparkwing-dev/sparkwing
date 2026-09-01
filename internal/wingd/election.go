package wingd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

var errPeerClosed = errors.New("wingd: peer closed connection")

func (d *Daemon) elect() (bool, error) {
	if err := d.layout.ensureDir(); err != nil {
		return false, err
	}
	f, err := os.OpenFile(d.layout.lock, os.O_CREATE|os.O_RDWR, 0o600)
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
	f, err := os.OpenFile(l.lock, os.O_RDWR, 0o600)
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
	if err := ValidateSocketPath(d.layout.sock); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(d.layout.sock), 0o700); err != nil {
		return nil, fmt.Errorf("wingd: prepare socket dir: %w", err)
	}
	_ = os.Remove(d.layout.sock)
	ln, err := net.Listen("unix", d.layout.sock)
	if err != nil {
		return nil, fmt.Errorf("wingd: listen %s: %w", d.layout.sock, err)
	}
	return ln, nil
}

type SocketPreparation uint8

const (
	SocketPreparationUnknown SocketPreparation = iota
	SocketPreparationElectionHeld
	SocketPreparationReady
	SocketPreparationCleanupFailed
)

func PrepareDaemonSocket(home string) (SocketPreparation, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return SocketPreparationUnknown, err
	}
	if err := l.ensureDir(); err != nil {
		return SocketPreparationUnknown, err
	}
	f, err := os.OpenFile(l.lock, os.O_CREATE|os.O_RDWR, 0o600)
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
	if err := os.Remove(l.sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SocketPreparationCleanupFailed, err
	}
	return SocketPreparationReady, nil
}
