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
	if err := ensureSocketDir(filepath.Dir(d.layout.sock)); err != nil {
		return nil, err
	}
	_ = os.Remove(d.layout.sock)
	ln, err := net.Listen("unix", d.layout.sock)
	if err != nil {
		return nil, fmt.Errorf("wingd: listen %s: %w", d.layout.sock, err)
	}
	// safety: the bound socket inherits the process umask, which can leave it
	// connectable by other accounts on platforms that honor socket modes.
	if err := os.Chmod(d.layout.sock, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("wingd: restrict socket %s: %w", d.layout.sock, err)
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
	// safety: holding the election lock proves no daemon serves this home, so a
	// socket left at the pre-upgrade path is stale and must not divert clients.
	for _, sock := range []string{l.sock, socketPathIn(tempSocketBaseDir(), l.home)} {
		if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
			return SocketPreparationCleanupFailed, err
		}
	}
	return SocketPreparationReady, nil
}
