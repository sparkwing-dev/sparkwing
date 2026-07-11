package wingd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// errPeerClosed marks an orderly peer disconnect (EOF), distinguished
// from a protocol or transport fault.
var errPeerClosed = errors.New("wingd: peer closed connection")

// elect attempts to win the single-daemon election for this home. It
// returns (true, nil) holding the lock when it wins, (false, nil) when
// another daemon holds it, or an error on a filesystem fault. The winner
// must call releaseLock when it stops serving.
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

// releaseLock unlocks and closes the election lock. Safe to call once,
// after a successful elect.
func (d *Daemon) releaseLock() {
	if d.lockFile == nil {
		return
	}
	_ = flockUnlock(d.lockFile)
	_ = d.lockFile.Close()
	d.lockFile = nil
}

// bindListener prepares the socket directory, records the resolved socket
// path under the home for discovery, removes any stale socket left by a
// dead predecessor (the election lock is held, so no live daemon owns it),
// and binds a fresh unix listener. It validates the path length first so
// an over-length socket fails with a named limit rather than a bare bind
// error.
func (d *Daemon) bindListener() (net.Listener, error) {
	if err := ValidateSocketPath(d.layout.sock); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(d.layout.sock), 0o700); err != nil {
		return nil, fmt.Errorf("wingd: prepare socket dir: %w", err)
	}
	_ = os.WriteFile(d.layout.record, []byte(d.layout.sock), 0o600)
	_ = os.Remove(d.layout.sock)
	ln, err := net.Listen("unix", d.layout.sock)
	if err != nil {
		return nil, fmt.Errorf("wingd: listen %s: %w", d.layout.sock, err)
	}
	return ln, nil
}

// RemoveStaleSocket removes home's socket file when no live daemon holds
// the election lock, and reports whether it did. Clients call it before
// spawning so a crashed daemon's leftover socket cannot make connect
// attempts fail forever. It never disturbs a live daemon: if the lock is
// held, it leaves the socket alone and returns false.
func RemoveStaleSocket(home string) (bool, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(l.lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()
	ok, err := flockTry(f)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	defer func() { _ = flockUnlock(f) }()
	if err := os.Remove(l.sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}
