package wingd

import (
	"fmt"
	"os"
	"time"
)

// RecoverUnreadableState preserves an unreadable state file under its
// quarantine name while holding the daemon election lock. The caller must
// obtain explicit operator consent before invoking it because unreadable
// bytes may describe guarded commands that are still running.
func RecoverUnreadableState(home string, now time.Time) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	if err := l.ensureDir(); err != nil {
		return "", err
	}
	f, err := os.OpenFile(l.lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("wingd: open lock %s: %w", l.lock, err)
	}
	defer func() { _ = f.Close() }()
	locked, err := flockTry(f)
	if err != nil {
		return "", fmt.Errorf("wingd: flock %s: %w", l.lock, err)
	}
	if !locked {
		return "", fmt.Errorf("wingd: daemon is running for home %s; stop it before recovering state", home)
	}
	defer func() { _ = flockUnlock(f) }()

	if _, _, _, _, err := readStateWithGuards(l.state); err == nil {
		return "", fmt.Errorf("wingd: state %s is readable; recovery refused", l.state)
	}
	quarantined, err := quarantineState(l.state, now)
	if err != nil {
		return "", fmt.Errorf("wingd: preserve unreadable state %s: %w", l.state, err)
	}
	if err := syncStateDirectory(l.dir); err != nil {
		return "", err
	}
	return quarantined, nil
}
