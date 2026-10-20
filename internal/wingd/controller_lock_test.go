package wingd

import (
	"strings"
	"testing"
)

func TestControllerAndDaemonShareProcessLock(t *testing.T) {
	home := t.TempDir()
	lock, err := AcquireControllerLock(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if second, err := AcquireControllerLock(home); err == nil || !strings.Contains(err.Error(), "already owns store directory") {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second controller: %v", err)
	}
	l, err := resolveLayout(home)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{layout: l}
	if won, err := d.elect(); err != nil || won {
		d.releaseLock()
		t.Fatalf("daemon overlapped controller: won=%t, err=%v", won, err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if won, err := d.elect(); err != nil || !won {
		t.Fatalf("daemon after controller released: won=%t, err=%v", won, err)
	}
	defer d.releaseLock()
	if second, err := AcquireControllerLock(home); err == nil {
		_ = second.Close()
		t.Fatal("controller overlapped daemon")
	}
}
